param([string]$ApiUrl = 'http://localhost:8080')
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'load-env.ps1')
if (-not $env:ADMIN_EMAIL -or -not $env:ADMIN_PASSWORD) { throw 'Set ADMIN_EMAIL and ADMIN_PASSWORD for the seeded admin before running the demo.' }
$walletDemoID = [guid]::NewGuid().ToString('N')
$walletDemoPassword = 'demo-' + [guid]::NewGuid().ToString('N')
function Call-Wallet([string]$Method, [string]$Path, $Body, [string]$Token = '', [string]$Key = '') {
    $walletHeaders = @{}
    if ($Token) { $walletHeaders.Authorization = 'Bearer ' + $Token }
    if ($Key) { $walletHeaders['Idempotency-Key'] = $Key }
    $walletArgs = @{ Method = $Method; Uri = ($ApiUrl + $Path); Headers = $walletHeaders }
    if ($null -ne $Body) { $walletArgs.Body = ($Body | ConvertTo-Json -Compress); $walletArgs.ContentType = 'application/json' }
    Invoke-RestMethod @walletArgs
}
function Assert-WalletBalance([long]$ID, [long]$Expected, [string]$Token) {
    $walletBalance = Call-Wallet GET "/wallets/$ID/balance" $null $Token
    if ($walletBalance.balance -ne $Expected) { throw "Wallet $ID balance $($walletBalance.balance), expected $Expected" }
}
$walletAlice = @{ email = "alice-$walletDemoID@example.com"; password = $walletDemoPassword }
$walletBob = @{ email = "bob-$walletDemoID@example.com"; password = $walletDemoPassword }
$null = Call-Wallet POST '/auth/register' $walletAlice
$null = Call-Wallet POST '/auth/register' $walletBob
$walletTokenA = (Call-Wallet POST '/auth/login' $walletAlice).access_token
$walletTokenB = (Call-Wallet POST '/auth/login' $walletBob).access_token
$walletA = Call-Wallet POST '/wallets' @{name='Demo A'} $walletTokenA "create-a-$walletDemoID"
$walletB = Call-Wallet POST '/wallets' @{name='Demo B'} $walletTokenB "create-b-$walletDemoID"
$walletPayload = @{ event_id="demo-$walletDemoID"; event='deposit.success'; wallet_id=$walletA.id; amount=1000000 } | ConvertTo-Json -Compress
$walletHmac = New-Object System.Security.Cryptography.HMACSHA256
try {
    $walletHmac.Key = [Text.Encoding]::UTF8.GetBytes($env:WEBHOOK_SECRET)
    $walletSignature = -join ($walletHmac.ComputeHash([Text.Encoding]::UTF8.GetBytes($walletPayload)) | ForEach-Object { $_.ToString('x2') })
} finally { $walletHmac.Dispose() }
$null = Invoke-RestMethod -Method Post -Uri "$ApiUrl/webhooks/mock-provider" -ContentType 'application/json' -Body $walletPayload -Headers @{'X-Signature'=$walletSignature}
Assert-WalletBalance $walletA.id 1000000 $walletTokenA
$walletTransferBody = @{ source_wallet_id=$walletA.id; destination_wallet_id=$walletB.id; amount=100000 }
$walletTransfer = Call-Wallet POST '/transfers' $walletTransferBody $walletTokenA "transfer-$walletDemoID"
$walletRetry = Call-Wallet POST '/transfers' $walletTransferBody $walletTokenA "transfer-$walletDemoID"
if ($walletTransfer.id -ne $walletRetry.id) { throw 'Retry created a second transfer' }
Assert-WalletBalance $walletA.id 900000 $walletTokenA
Assert-WalletBalance $walletB.id 100000 $walletTokenB
$walletAdminToken = (Call-Wallet POST '/auth/login' @{email=$env:ADMIN_EMAIL;password=$env:ADMIN_PASSWORD}).access_token
$null = Call-Wallet POST "/admin/transactions/$($walletTransfer.id)/reverse" @{reason='Demo full refund'} $walletAdminToken "reverse-$walletDemoID"
Assert-WalletBalance $walletA.id 1000000 $walletTokenA
Assert-WalletBalance $walletB.id 0 $walletTokenB
$walletReport = Call-Wallet POST '/admin/reconciliations' @{} $walletAdminToken "reconcile-$walletDemoID"
if ($walletReport.status -ne 'pass') { throw ($walletReport | ConvertTo-Json -Depth 10) }
Write-Output "PASS: deposit, transfer, retry, reversal, reconciliation; A=$($walletA.id), B=$($walletB.id), transaction=$($walletTransfer.id)"

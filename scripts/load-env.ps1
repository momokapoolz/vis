# Dot-source this script for local Go commands. Values are literal, never evaluated.
$walletRoot = Split-Path -Parent $PSScriptRoot
$walletEnv = Join-Path $walletRoot '.env'
if (-not (Test-Path -LiteralPath $walletEnv)) { throw 'Copy .env.example to .env first.' }
$walletNames = @('POSTGRES_PASSWORD', 'APP_DB_PASSWORD', 'DATABASE_URL', 'MIGRATION_DATABASE_URL', 'JWT_SECRET', 'WEBHOOK_SECRET', 'LISTEN_ADDR')
foreach ($walletLine in Get-Content -LiteralPath $walletEnv) {
    if ($walletLine -match '^([A-Z_]+)=(.*)$' -and $Matches[1] -in $walletNames) {
        Set-Item -LiteralPath ('Env:' + $Matches[1]) -Value $Matches[2]
    }
}

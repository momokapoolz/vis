$ErrorActionPreference = 'Stop'
# Task Scheduler uses the host timezone; validate instead of silently scheduling the wrong hour.
if ((Get-TimeZone).Id -ne 'SE Asia Standard Time') {
    throw 'This schedule expects Windows timezone SE Asia Standard Time (UTC+07:00). Set the desired schedule manually on other hosts.'
}
$walletScript = Join-Path $PSScriptRoot 'reconcile.ps1'
$walletAction = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument ('-NoProfile -NonInteractive -WindowStyle Hidden -File "' + $walletScript + '"')
$walletTrigger = New-ScheduledTaskTrigger -Daily -At '00:00'
$walletSettings = New-ScheduledTaskSettingsSet -StartWhenAvailable -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName 'WalletPoC-Reconciliation' -Action $walletAction -Trigger $walletTrigger -Settings $walletSettings -Description 'Check simulated wallet ledger nightly. Requires Docker Desktop running.'

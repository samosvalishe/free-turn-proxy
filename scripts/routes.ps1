# Usage: .\client.exe -debug ... 2>&1 | .\scripts\routes.ps1
# Requires Administrator privileges.

$gateway = Get-NetRoute `
    -DestinationPrefix "0.0.0.0/0" `
    | Sort-Object RouteMetric `
    | Select-Object -First 1 -ExpandProperty NextHop

if (-not $gateway) {
    Write-Error "Could not determine default gateway"
    exit 1
}

Write-Host "Default gateway: $gateway"

$input | ForEach-Object {
    $line = $_.Trim()
    if ($line -eq "") { return }

    $prefix = $null

    if ($line -match 'TURN server IP: ((\d{1,3}\.){3}\d{1,3})') {
        $prefix = "$($Matches[1])/32"
    } elseif ($line -match 'relayed-address=((\d{1,3}\.){3}\d{1,3}):\d+') {
        $prefix = "$($Matches[1])/32"
    } elseif ($line -match '^((\d{1,3}\.){3}\d{1,3}/\d{1,2})$') {
        $prefix = $Matches[1]
    } elseif ($line -match '^((\d{1,3}\.){3}\d{1,3})$') {
        $prefix = "$line/32"
    }

    if (-not $prefix) { return }

    $existingRoutes = @(Get-NetRoute `
        -DestinationPrefix $prefix `
        -PolicyStore ActiveStore `
        -ErrorAction SilentlyContinue)

    if ($existingRoutes | Where-Object { $_.NextHop -eq $gateway }) {
        Write-Host "Route to $prefix via $gateway already exists"
        return
    }

    if ($existingRoutes.Count -gt 0) {
        Write-Host "Updating route to $prefix via $gateway"
        $existingRoutes | Remove-NetRoute -Confirm:$false -ErrorAction Stop
    } else {
        Write-Host "Ensuring route to $prefix via $gateway"
    }

    New-NetRoute `
        -DestinationPrefix $prefix `
        -NextHop $gateway `
        -PolicyStore ActiveStore `
        -ErrorAction Stop | Out-Null
}

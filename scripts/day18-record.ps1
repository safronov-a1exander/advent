<#
.SYNOPSIS
  Запись демо дня 18: сервер слежения живёт отдельно, агент будится таймером.

.DESCRIPTION
  Сервер «Курсы» запускается фоном по HTTP (он и должен жить отдельно от
  агента), затем в одну запись идут три прогона:

    1) чат заводит слежение за валютами;
    2) advent watch — агент сам собирает сводку каждые Every;
    3) список заданий напрямую у сервера: замеры накопились без агента.

  В конце сервер останавливается. Задания и замеры остаются в Data —
  следующий запуск сервера продолжит с них.

.EXAMPLE
  .\scripts\day18-record.ps1                    # живой API
  .\scripts\day18-record.ps1 -Provider mock -Market walk   # репетиция
#>
[CmdletBinding()]
param(
  [string]$Provider = '',
  [string]$Market = 'coinbase',
  [string]$Every = '1m',
  [int]$Count = 3,
  [string]$Data = 'runs/mcp/rates-watch-day18.json',
  [string]$Name = 'day18'
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
Push-Location $repo
try {
  & go build -o bin\advent.exe ./cmd/advent
  if ($LASTEXITCODE -ne 0) { throw "go build упал" }
  Remove-Item -Force $Data -ErrorAction SilentlyContinue
  Remove-Item -Recurse -Force runs\sessions-day18 -ErrorAction SilentlyContinue
  Remove-Item -Force runs\digests-day18.md -ErrorAction SilentlyContinue

  $srv = Start-Process -FilePath (Join-Path $repo 'bin\advent.exe') -WindowStyle Hidden -PassThru `
    -ArgumentList @('mcp-server','rates','-http','127.0.0.1:8765','-market',$Market,'-data',$Data,'-log') `
    -RedirectStandardError (Join-Path $repo 'runs\mcp-rates-day18.log')
  Start-Sleep -Seconds 2

  $prov = @()
  if ($Provider) { $prov = @('-provider', $Provider) }
  $t = 'advent · день 18 — сводка по расписанию'
  $chat = @('demo','-script','scripts/day18.demo','-mcp','rates-live','-thinking','disabled','-sessions','runs/sessions-day18','-new','-title',$t) + $prov
  $watch = @('watch','-every',$Every,'-count',"$Count",'-hold','10s','-out','runs/digests-day18.md') + $prov
  $list = @('mcp','-server','rates-live','-call','list_watches','-hold','15s')
  & "$PSScriptRoot\demo.ps1" -Name $Name -SkipBuild -MaxSeconds 1800 -AppArgsList @($chat, $watch, $list)
}
finally {
  if ($srv -and -not $srv.HasExited) { Stop-Process -Id $srv.Id -Force }
  Pop-Location
}

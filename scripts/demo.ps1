<#
.SYNOPSIS
  Полный цикл «запись демо»: собрать → открыть окно фиксированного размера →
  начать запись экрана → прогнать сценарий → остановить запись.

.DESCRIPTION
  На выходе один mp4-файл с непрерывной записью окна.
  Сценарий прогоняется самим приложением (`advent demo -script ...`),
  никакой эмуляции клавиш извне — поэтому запись воспроизводима.

.EXAMPLE
  .\scripts\demo.ps1 -Name day01 -AppArgs @('demo','-script','scripts/day01.demo')

.EXAMPLE
  # без сценария, просто записать ручной прогон 3 минуты
  .\scripts\demo.ps1 -Name manual -AppArgs @('chat') -MaxSeconds 180

.EXAMPLE
  # два прогона подряд в одно непрерывное видео
  .\scripts\demo.ps1 -Name day04 -AppArgsList @(
    @('lab','-scenario','scenarios/a.yaml','-script','scripts/a.demo'),
    @('lab','-scenario','scenarios/b.yaml','-script','scripts/b.demo'))
#>
[CmdletBinding()]
param(
  [Parameter(Mandatory=$true)][string]$Name,
  [string[]]$AppArgs = @('--help'),
  # Несколько запусков подряд в ОДНУ непрерывную запись.
  # Каждый элемент — массив аргументов: @(@('lab','-scenario','a.yaml'), @('lab','-scenario','b.yaml'))
  [object[]]$AppArgsList,
  [int]$Cols = 150,
  [int]$Rows = 40,
  [int]$X = 60,
  [int]$Y = 30,
  [int]$Width = 1680,
  # Высота подобрана так, чтобы нижний край окна прошёл ВЫШЕ водяного знака
  # «Активация Windows» в правом нижнем углу экрана — иначе он лезет в кадр.
  [int]$Height = 900,
  [int]$Fps = 30,
  [int]$MaxSeconds = 900,
  # Сколько секунд отрезать с конца: в хвосте видно, как закрывается окно
  # терминала и что было под ним. 0 — не обрезать.
  [double]$TrimTail = 3.0,
  [switch]$SkipBuild,
  [string]$OutDir = "recordings"
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
. "$PSScriptRoot\win.ps1"

if (-not (Test-Path (Join-Path $repo $OutDir))) {
  New-Item -ItemType Directory -Force -Path (Join-Path $repo $OutDir) | Out-Null
}
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$out = Join-Path $repo "$OutDir\$Name-$stamp.mp4"

# --- 1. сборка ---
$exe = Join-Path $repo 'bin\advent.exe'
if (-not $SkipBuild) {
  Write-Host "== сборка ==" -ForegroundColor Cyan
  Push-Location $repo
  try { & go build -o $exe ./cmd/advent; if ($LASTEXITCODE -ne 0) { throw "go build упал" } }
  finally { Pop-Location }
}
if (-not (Test-Path $exe)) { throw "не найден $exe" }

# --- 2. что именно запускаем ---
# PowerShell разворачивает @(@('a','b')) в @('a','b'), поэтому одиночный
# прогон приезжает сюда как плоский список строк. Заворачиваем обратно,
# иначе каждый аргумент будет принят за отдельный запуск.
if (-not $AppArgsList) {
  $AppArgsList = @(,$AppArgs)
} elseif (-not ($AppArgsList | Where-Object { $_ -isnot [string] })) {
  $AppArgsList = @(,[string[]]$AppArgsList)
}
# Заголовок окна виден в записи, поэтому он тоже часть подачи:
# на экране должен быть продукт, а не служебная метка прогона.
$marker = "Бюджет - ассистент по личным тратам"

function Start-AdventWindow {
  # TailSeconds = 0: после выхода из TUI окно закрывается сразу, иначе
  # в кадре повисает голый терминал.
  param([string[]]$RunArgs, [int]$TailSeconds = 0)
  $argLine = ($RunArgs | ForEach-Object { "'" + ($_ -replace "'","''") + "'" }) -join ','
  $inner = @"
`$Host.UI.RawUI.WindowTitle = '$marker'
try { `$Host.UI.RawUI.BufferSize = New-Object Management.Automation.Host.Size($Cols, 3000) } catch {}
try { `$Host.UI.RawUI.WindowSize = New-Object Management.Automation.Host.Size($Cols, $Rows) } catch {}
Set-Location '$repo'
& '$exe' @($argLine)
Start-Sleep -Seconds $TailSeconds
"@
  $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($inner))
  Start-Process -FilePath 'powershell.exe' `
    -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-EncodedCommand',$encoded `
    -PassThru
}

Write-Host "== запуск окна (прогонов: $($AppArgsList.Count)) ==" -ForegroundColor Cyan
$app = Start-AdventWindow -RunArgs ([string[]]$AppArgsList[0])

$hwnd = Wait-AdventWindow -TitleContains $marker -TimeoutSec 30
$rect = Set-AdventWindowRect -Handle $hwnd -X $X -Y $Y -Width $Width -Height $Height
Write-Host "область записи: $($rect.X),$($rect.Y) $($rect.W)x$($rect.H)"
Hide-MousePointer
Start-Sleep -Milliseconds 600

# --- 3. запись ---
Write-Host "== старт записи -> $out ==" -ForegroundColor Cyan
$rec = & "$PSScriptRoot\record.ps1" `
  -Region "$($rect.X),$($rect.Y),$($rect.W),$($rect.H)" `
  -Out $out -Fps $Fps -NoWait

# --- 4. прогоны один за другим; запись при этом не прерывается ---
for ($i = 0; $i -lt $AppArgsList.Count; $i++) {
  if ($i -gt 0) {
    $app = Start-AdventWindow -RunArgs ([string[]]$AppArgsList[$i])
    $hwnd = Wait-AdventWindow -TitleContains $marker -TimeoutSec 30
    [void](Set-AdventWindowRect -Handle $hwnd -X $X -Y $Y -Width $Width -Height $Height)
    Hide-MousePointer
  }
  if (-not $app.WaitForExit($MaxSeconds * 1000)) {
    Write-Warning "прогон $($i+1) не завершился за ${MaxSeconds}s"
    try { $app.CloseMainWindow() | Out-Null } catch {}
  }
}
Start-Sleep -Milliseconds 900   # хвост, чтобы финальный кадр попал в видео

# --- 5. стоп ---
& "$PSScriptRoot\stop-recording.ps1" -Process $rec

if (Test-Path $out) {
  # --- 6. обрезаем хвост ---
  # В конце записи видно, как закрывается окно терминала и что было под ним.
  # Режем перекодированием: -c copy обрубает по ближайшему ключевому кадру,
  # и хвост частично остаётся.
  if ($TrimTail -gt 0) {
    $full = [double](& ffprobe -v error -show_entries format=duration -of default=nw=1:nk=1 $out)
    $keep = $full - $TrimTail
    if ($keep -gt 1) {
      $tmp = [System.IO.Path]::ChangeExtension($out, 'trim.mp4')
      & ffmpeg -hide_banner -loglevel error -y -i $out -t $keep `
        -c:v libx264 -preset veryfast -crf 20 -pix_fmt yuv420p -movflags +faststart $tmp
      if ($LASTEXITCODE -eq 0 -and (Test-Path $tmp)) {
        Remove-Item $out -Force
        Rename-Item $tmp $out
      } else {
        Write-Warning "не удалось обрезать хвост, оставляю исходный файл"
        if (Test-Path $tmp) { Remove-Item $tmp -Force }
      }
    }
  }

  $bytes = (Get-Item $out).Length
  $size = [math]::Round($bytes / 1MB, 1)
  $dur = & ffprobe -v error -show_entries format=duration -of default=nw=1:nk=1 $out 2>$null
  # Пустой mp4 (только заголовок) весит пару сотен байт: ловим такое сразу,
  # иначе битая запись обнаружится уже при монтаже.
  if ($bytes -lt 100KB -or -not $dur) {
    Write-Warning "запись выглядит битой: $out ($bytes байт, длительность '$dur'). Перезапиши этот день."
  } else {
    Write-Host "готово: $out  (${size} МБ, ${dur} с)" -ForegroundColor Green
  }
} else {
  Write-Warning "файл не создан: $out"
}

<#
.SYNOPSIS
  Запись видео окна приложения через ffmpeg.

.DESCRIPTION
  Пишет ПОЛНОЦЕННОЕ видео (не нарезку кадров) области экрана, занятой окном.

  Почему область экрана, а не `-i title=...`:
  gdigrab с захватом по заголовку отдаёт чёрный кадр для окон, которые
  рисуются через DirectX (Windows Terminal, современный conhost). Поэтому
  по умолчанию используется ddagrab (Desktop Duplication API) + crop по
  прямоугольнику окна. Это работает для любого окна и даёт 60 fps без
  нагрузки на CPU.

.PARAMETER Region
  Явный прямоугольник "X,Y,W,H". Если не задан — берётся из окна.

.EXAMPLE
  .\scripts\record.ps1 -WindowTitle "advent" -Out recordings\day01.mp4 -Seconds 120

.EXAMPLE
  # запись до ручной остановки (Ctrl+C или Stop-Recording)
  $r = .\scripts\record.ps1 -Region "80,60,1600,900" -Out recordings\demo.mp4 -NoWait
#>
[CmdletBinding()]
param(
  [string]$WindowTitle,
  [string]$Region,
  [Parameter(Mandatory=$true)][string]$Out,
  [int]$Fps = 30,
  [int]$Seconds = 0,
  [ValidateSet('ddagrab','gdigrab-desktop','gdigrab-title')][string]$Grabber = 'ddagrab',
  [switch]$NoWait,
  [string]$AudioDevice
)

$ErrorActionPreference = 'Stop'
. "$PSScriptRoot\win.ps1"

if (-not (Get-Command ffmpeg -ErrorAction SilentlyContinue)) {
  throw "ffmpeg не найден в PATH. Установи: winget install Gyan.FFmpeg"
}

$outDir = Split-Path -Parent $Out
if ($outDir -and -not (Test-Path $outDir)) { New-Item -ItemType Directory -Force -Path $outDir | Out-Null }

# --- определяем область захвата ---
if ($Region) {
  $p = $Region -split ','
  if ($p.Count -ne 4) { throw "Region должен быть в формате X,Y,W,H" }
  $rect = [pscustomobject]@{ X=[int]$p[0]; Y=[int]$p[1]; W=[int]$p[2]; H=[int]$p[3] }
} elseif ($WindowTitle) {
  $h = Wait-AdventWindow -TitleContains $WindowTitle
  $rect = Set-AdventWindowRect -Handle $h
  Write-Host "окно '$([Advent.Win]::TitleOf($h))' -> $($rect.X),$($rect.Y) $($rect.W)x$($rect.H)"
} else {
  throw "Задай -WindowTitle или -Region"
}

# ffmpeg требует чётные размеры для h264 yuv420p
$w = $rect.W - ($rect.W % 2)
$h2 = $rect.H - ($rect.H % 2)

$args = @('-hide_banner', '-loglevel', 'warning', '-y')

switch ($Grabber) {
  'ddagrab' {
    # Desktop Duplication: захват на GPU, затем crop нужной области.
    $args += @(
      '-init_hw_device','d3d11va',
      '-filter_complex', "ddagrab=output_idx=0:framerate=$Fps,hwdownload,format=bgra,crop=${w}:${h2}:$($rect.X):$($rect.Y)"
    )
  }
  'gdigrab-desktop' {
    $args += @(
      '-f','gdigrab','-framerate',"$Fps",
      '-offset_x',"$($rect.X)",'-offset_y',"$($rect.Y)",
      '-video_size',"${w}x${h2}",'-i','desktop'
    )
  }
  'gdigrab-title' {
    if (-not $WindowTitle) { throw "gdigrab-title требует -WindowTitle" }
    $args += @('-f','gdigrab','-framerate',"$Fps",'-i',"title=$WindowTitle")
  }
}

if ($AudioDevice) {
  $args += @('-f','dshow','-i',"audio=$AudioDevice",'-c:a','aac','-b:a','128k')
}

if ($Seconds -gt 0) { $args += @('-t', "$Seconds") }

$args += @(
  '-c:v','libx264','-preset','veryfast','-crf','20',
  '-pix_fmt','yuv420p','-movflags','+faststart',
  $Out
)

Write-Host "ffmpeg $($args -join ' ')"

if ($NoWait) {
  # stdin в pipe: остановка отправкой 'q' (см. Stop-Recording ниже)
  $psi = New-Object System.Diagnostics.ProcessStartInfo
  $psi.FileName = (Get-Command ffmpeg).Source
  $psi.Arguments = ($args | ForEach-Object { if ($_ -match '\s') { '"' + $_ + '"' } else { $_ } }) -join ' '
  $psi.RedirectStandardInput = $true
  $psi.UseShellExecute = $false
  $proc = [System.Diagnostics.Process]::Start($psi)
  Start-Sleep -Milliseconds 800   # даём ffmpeg подняться до старта демо
  return $proc
} else {
  & ffmpeg @args
  Write-Host "готово: $Out"
}

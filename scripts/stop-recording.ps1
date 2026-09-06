# Корректно останавливает ffmpeg, запущенный record.ps1 -NoWait.
# Посылает 'q' в stdin — ffmpeg дописывает moov-атом и файл остаётся валидным.
# Kill-ом убивать нельзя: получится битый mp4.
[CmdletBinding()]
param(
  [Parameter(Mandatory=$true)][System.Diagnostics.Process]$Process,
  [int]$TimeoutSec = 20
)

$ErrorActionPreference = 'Stop'

if ($Process.HasExited) { Write-Host "ffmpeg уже завершён"; return }

try {
  $Process.StandardInput.WriteLine('q')
  $Process.StandardInput.Flush()
} catch {
  Write-Warning "не удалось написать в stdin ffmpeg: $_"
}

if (-not $Process.WaitForExit($TimeoutSec * 1000)) {
  Write-Warning "ffmpeg не завершился за ${TimeoutSec}s, шлю CloseMainWindow"
  [void]$Process.CloseMainWindow()
  if (-not $Process.WaitForExit(5000)) {
    Write-Warning "принудительно убиваю ffmpeg — файл может быть повреждён"
    $Process.Kill()
  }
}
Write-Host "запись остановлена (exit $($Process.ExitCode))"

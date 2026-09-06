<#
.SYNOPSIS
  Записать видео по всем дням за один заход.

.DESCRIPTION
  Для каждого дня: переключиться на его ветку, собрать бинарь, прогнать
  демо-сценарий и записать окно в отдельный mp4. В конце возвращается
  ветка, на которой всё начиналось.

  Требует чистое рабочее дерево — иначе checkout между ветками потеряет
  незакоммиченные правки. Скрипт проверяет это и отказывается работать.

  Перед запуском обязательно:
    * DEEPSEEK_API_KEY выставлен (или заполнен config.local.yaml)
    * `advent doctor` проходит
    * имена моделей в config.yaml сверены с `advent models`

.PARAMETER Days
  Какие дни писать. По умолчанию все, для которых есть ветка.

.PARAMETER DryRun
  Ничего не записывать, только показать план.

.EXAMPLE
  .\scripts\record-all.ps1

.EXAMPLE
  .\scripts\record-all.ps1 -Days 4,5
#>
[CmdletBinding()]
param(
  [int[]]$Days,
  # Прогнать на другом провайдере из config.yaml. Удобно для репетиции:
  # -Provider mock вместе с `go run ./tools/mockllm` не тратит токены.
  [string]$Provider,
  [switch]$DryRun,
  [switch]$SkipDoctor,
  [int]$Fps = 30
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
Push-Location $repo

# --- план: что и чем записывать ---
# Ключ — номер дня. MaxSeconds с запасом: реальные вызовы к API медленнее заглушки.
$plan = @{
  1 = @{
    Branch = 'day-01'
    MaxSeconds = 900
    Runs = @(
      ,@('demo','-script','scripts/day01.demo')
    )
  }
  2 = @{
    Branch = 'day-02'
    MaxSeconds = 1200
    Runs = @(
      ,@('lab','-scenario','scenarios/day02-format.yaml','-script','scripts/day02.demo')
    )
  }
  3 = @{
    Branch = 'day-03'
    MaxSeconds = 2400
    Runs = @(
      @('lab','-scenario','scenarios/day03-reasoning.yaml','-script','scripts/day03.demo'),
      @('demo','-script','scripts/day03-chat.demo')
    )
  }
  4 = @{
    Branch = 'day-04'
    MaxSeconds = 2400
    Runs = @(
      @('lab','-scenario','scenarios/day04-temperature-creative.yaml','-script','scripts/day04-creative.demo'),
      @('lab','-scenario','scenarios/day04-temperature-precise.yaml','-script','scripts/day04-precise.demo'),
      @('demo','-script','scripts/day04-chat.demo')
    )
  }
  5 = @{
    Branch = 'day-05'
    MaxSeconds = 2400
    Runs = @(
      @('lab','-scenario','scenarios/day05-models.yaml','-script','scripts/day05.demo'),
      @('demo','-script','scripts/day05-chat.demo')
    )
  }
}

if (-not $Days) { $Days = $plan.Keys | Sort-Object }

# --- проверки ---
$dirty = git status --porcelain
if ($dirty) {
  Pop-Location
  throw "Рабочее дерево грязное — закоммить или спрячь правки перед записью:`n$dirty"
}
$startBranch = (git rev-parse --abbrev-ref HEAD).Trim()
Write-Host "стартовая ветка: $startBranch" -ForegroundColor Cyan

foreach ($d in $Days) {
  if (-not $plan.ContainsKey([int]$d)) { throw "нет плана для дня $d" }
  $b = $plan[[int]$d].Branch
  git rev-parse --verify --quiet "refs/heads/$b" > $null
  if (-not $?) { Pop-Location; throw "нет ветки $b" }
}

if (-not $SkipDoctor) {
  Write-Host "== проверка доступа к API ==" -ForegroundColor Cyan
  $doctorArgs = @('run','./cmd/advent','doctor')
  if ($Provider) { $doctorArgs += @('-provider', $Provider) }
  & go @doctorArgs
  if ($LASTEXITCODE -ne 0) {
    Pop-Location
    throw "advent doctor не прошёл — ключ или конфиг не готовы"
  }
}

$results = @()
try {
  foreach ($d in $Days) {
    $day = $plan[[int]$d]
    $name = 'day{0:d2}' -f [int]$d
    Write-Host ""
    Write-Host "===== ДЕНЬ $d ($($day.Branch)) =====" -ForegroundColor Yellow

    $runs = $day.Runs
    if ($Provider) {
      $runs = @($day.Runs | ForEach-Object { ,($_ + @('-provider', $Provider)) })
    }

    if ($DryRun) {
      foreach ($r in $runs) { Write-Host "  advent $($r -join ' ')" }
      continue
    }

    git checkout --quiet $day.Branch
    if ($LASTEXITCODE -ne 0) { throw "не смог переключиться на $($day.Branch)" }

    & "$PSScriptRoot\demo.ps1" -Name $name -AppArgsList $runs `
      -MaxSeconds $day.MaxSeconds -Fps $Fps

    $last = Get-ChildItem (Join-Path $repo 'recordings') -Filter "$name-*.mp4" |
            Sort-Object LastWriteTime | Select-Object -Last 1
    $results += [pscustomobject]@{ День = $d; Ветка = $day.Branch; Файл = $last.Name }
  }
}
finally {
  git checkout --quiet $startBranch
  Pop-Location
}

if (-not $DryRun) {
  Write-Host ""
  Write-Host "== записано ==" -ForegroundColor Green
  $results | Format-Table -AutoSize
  Write-Host "вернулись на ветку $startBranch"
}

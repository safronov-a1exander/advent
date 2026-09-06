# Общие помощники для работы с окнами Windows (P/Invoke user32).
# Подключается точкой:  . "$PSScriptRoot\win.ps1"

if (-not ('Advent.Win' -as [type])) {
@"
using System;
using System.Text;
using System.Collections.Generic;
using System.Runtime.InteropServices;

namespace Advent {
  public struct RECT { public int Left, Top, Right, Bottom; }

  public static class Win {
    [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
    [DllImport("user32.dll")] public static extern bool MoveWindow(IntPtr h, int x, int y, int w, int ht, bool repaint);
    [DllImport("user32.dll")] public static extern bool SetWindowPos(IntPtr h, IntPtr after, int x, int y, int w, int ht, uint flags);
    [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();

    // SetForegroundWindow из фонового процесса Windows нередко игнорирует —
    // окно остаётся под чужим, и запись экрана снимает не то. Поэтому на
    // время демонстрации явно поднимаем окно поверх всех (HWND_TOPMOST).
    public static void PlaceOnTop(IntPtr h, int x, int y, int w, int ht) {
      IntPtr HWND_TOPMOST = new IntPtr(-1);
      const uint SWP_SHOWWINDOW = 0x0040;
      SetWindowPos(h, HWND_TOPMOST, x, y, w, ht, SWP_SHOWWINDOW);
    }
    [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
    [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr h, int cmd);
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern int GetWindowTextW(IntPtr h, StringBuilder s, int max);
    [DllImport("user32.dll")] public static extern int GetWindowTextLength(IntPtr h);
    [DllImport("user32.dll")] public static extern bool EnumWindows(EnumProc cb, IntPtr p);
    [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
    [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
    [DllImport("dwmapi.dll")] public static extern int DwmGetWindowAttribute(IntPtr h, int attr, out RECT r, int size);

    // GetWindowRect на Windows 10/11 возвращает прямоугольник ВМЕСТЕ с невидимой
    // рамкой изменения размера (несколько пикселей по бокам и снизу). Если писать
    // видео по нему, в кадр попадает полоска того, что лежит позади окна.
    // DWMWA_EXTENDED_FRAME_BOUNDS = 9 отдаёт реально видимые границы.
    public static RECT VisibleRect(IntPtr h) {
      RECT r;
      if (DwmGetWindowAttribute(h, 9, out r, Marshal.SizeOf(typeof(RECT))) == 0 &&
          r.Right > r.Left && r.Bottom > r.Top) {
        return r;
      }
      GetWindowRect(h, out r);
      return r;
    }

    public delegate bool EnumProc(IntPtr h, IntPtr p);

    public static List<IntPtr> ByPid(uint pid) {
      var found = new List<IntPtr>();
      EnumWindows((h, p) => {
        uint wpid; GetWindowThreadProcessId(h, out wpid);
        if (wpid == pid && IsWindowVisible(h) && GetWindowTextLength(h) > 0) found.Add(h);
        return true;
      }, IntPtr.Zero);
      return found;
    }

    public static List<IntPtr> ByTitle(string needle) {
      var found = new List<IntPtr>();
      EnumWindows((h, p) => {
        int len = GetWindowTextLength(h);
        if (len == 0 || !IsWindowVisible(h)) return true;
        var sb = new StringBuilder(len + 1);
        GetWindowTextW(h, sb, sb.Capacity);
        if (sb.ToString().IndexOf(needle, StringComparison.OrdinalIgnoreCase) >= 0) found.Add(h);
        return true;
      }, IntPtr.Zero);
      return found;
    }

    public static string TitleOf(IntPtr h) {
      int len = GetWindowTextLength(h);
      var sb = new StringBuilder(len + 1);
      GetWindowTextW(h, sb, sb.Capacity);
      return sb.ToString();
    }
  }
}
"@ | ForEach-Object { Add-Type -TypeDefinition $_ -Language CSharp }
}

function Wait-AdventWindow {
  param(
    [Parameter(Mandatory=$true)][string]$TitleContains,
    [int]$TimeoutSec = 25
  )
  $deadline = (Get-Date).AddSeconds($TimeoutSec)
  while ((Get-Date) -lt $deadline) {
    $hits = [Advent.Win]::ByTitle($TitleContains)
    if ($hits.Count -gt 0) { return $hits[0] }
    Start-Sleep -Milliseconds 250
  }
  throw "Окно с заголовком, содержащим '$TitleContains', не появилось за $TimeoutSec с"
}

function Set-AdventWindowRect {
  param(
    [Parameter(Mandatory=$true)][IntPtr]$Handle,
    [int]$X = 80, [int]$Y = 60, [int]$Width = 1600, [int]$Height = 900
  )
  [void][Advent.Win]::ShowWindow($Handle, 9)   # SW_RESTORE
  [Advent.Win]::PlaceOnTop($Handle, $X, $Y, $Width, $Height)
  [void][Advent.Win]::SetForegroundWindow($Handle)
  Start-Sleep -Milliseconds 500

  # Проверяем, что окно действительно наверху: иначе запись снимет чужое окно,
  # и это выяснится только при просмотре готового видео.
  if ([Advent.Win]::GetForegroundWindow() -ne $Handle) {
    Write-Warning "окно демо не стало активным — запись может поймать чужое окно"
  }
  $r = [Advent.Win]::VisibleRect($Handle)
  [pscustomobject]@{
    X = $r.Left; Y = $r.Top
    W = $r.Right - $r.Left
    H = $r.Bottom - $r.Top
  }
}

function Hide-MousePointer {
  # Уводим курсор в правый нижний угол экрана, чтобы он не мельтешил в кадре.
  Add-Type -AssemblyName System.Windows.Forms
  $b = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
  [void][Advent.Win]::SetCursorPos($b.Right - 1, $b.Bottom - 1)
}

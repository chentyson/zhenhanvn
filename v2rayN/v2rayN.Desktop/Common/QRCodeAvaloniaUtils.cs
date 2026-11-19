using System.Runtime.InteropServices;
using SkiaSharp;

namespace v2rayN.Desktop.Common;

public partial class QRCodeAvaloniaUtils
{
    public static byte[]? CaptureScreen()
    {
        if (!Utils.IsWindows())
        {
            return null;
        }

        try
        {
            return CaptureScreenWindows();
        }
        catch (Exception ex)
        {
            Logging.SaveLog("CaptureScreen", ex);
            return null;
        }
    }

    [SupportedOSPlatform("windows")]
    private static byte[]? CaptureScreenWindows()
    {
        var hdcScreen = IntPtr.Zero;
        var hdcMemory = IntPtr.Zero;
        var hBitmap = IntPtr.Zero;

        try
        {
            // Capture the entire virtual screen (all monitors) so a QR code can be
            // displayed anywhere on any display.
            var left = GetSystemMetrics(SM_XVIRTUALSCREEN);
            var top = GetSystemMetrics(SM_YVIRTUALSCREEN);
            var width = GetSystemMetrics(SM_CXVIRTUALSCREEN);
            var height = GetSystemMetrics(SM_CYVIRTUALSCREEN);

            if (width <= 0 || height <= 0)
            {
                // Fallback to the primary work area.
                var workArea = new RECT();
                SystemParametersInfo(SPI_GETWORKAREA, 0, ref workArea, 0);
                left = workArea.Left;
                top = workArea.Top;
                width = workArea.Right - workArea.Left;
                height = workArea.Bottom - workArea.Top;
            }

            hdcScreen = GetDC(IntPtr.Zero);
            if (hdcScreen == IntPtr.Zero)
            {
                return null;
            }

            hdcMemory = CreateCompatibleDC(hdcScreen);
            hBitmap = CreateCompatibleBitmap(hdcScreen, width, height);

            if (hBitmap == IntPtr.Zero)
            {
                return null;
            }

            SelectObject(hdcMemory, hBitmap);

            const int SRCCOPY = 0x00CC0020;
            BitBlt(hdcMemory, 0, 0, width, height, hdcScreen, left, top, SRCCOPY);

            var bmi = new BITMAPINFO
            {
                biSize = Marshal.SizeOf(typeof(BITMAPINFO)),
                biWidth = width,
                biHeight = -height,
                biPlanes = 1,
                biBitCount = 32,
                biCompression = 0
            };

            var imageSize = width * height * 4;
            var imageData = new byte[imageSize];

            var scanLines = GetDIBits(hdcScreen, hBitmap, 0, (uint)height, imageData, ref bmi, 0);

            if (scanLines == 0)
            {
                return null;
            }

            using var bitmap = new SKBitmap(width, height, SKColorType.Bgra8888, SKAlphaType.Premul);
            Marshal.Copy(imageData, 0, bitmap.GetPixels(), imageSize);

            using var image = SKImage.FromBitmap(bitmap);
            using var encoded = image.Encode(SKEncodedImageFormat.Png, 100);
            return encoded.ToArray();
        }
        catch (Exception ex)
        {
            Logging.SaveLog("CaptureScreenWindows", ex);
            return null;
        }
        finally
        {
            if (hBitmap != IntPtr.Zero)
            {
                DeleteObject(hBitmap);
            }

            if (hdcMemory != IntPtr.Zero)
            {
                DeleteDC(hdcMemory);
            }

            if (hdcScreen != IntPtr.Zero)
            {
                ReleaseDC(IntPtr.Zero, hdcScreen);
            }
        }
    }

    #region Win32 API

    [LibraryImport("user32.dll")]
    private static partial IntPtr GetDC(IntPtr hwnd);

    [LibraryImport("user32.dll")]
    private static partial int ReleaseDC(IntPtr hwnd, IntPtr hdc);

    [LibraryImport("gdi32.dll")]
    private static partial IntPtr CreateCompatibleDC(IntPtr hdc);

    [LibraryImport("gdi32.dll")]
    private static partial IntPtr CreateCompatibleBitmap(IntPtr hdc, int nWidth, int nHeight);

    [LibraryImport("gdi32.dll")]
    private static partial IntPtr SelectObject(IntPtr hdc, IntPtr hObject);

    [LibraryImport("gdi32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool BitBlt(IntPtr hdcDest, int nXDest, int nYDest, int nWidth, int nHeight,
        IntPtr hdcSrc, int nXSrc, int nYSrc, int dwRop);

    [LibraryImport("gdi32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool DeleteObject(IntPtr hObject);

    [LibraryImport("gdi32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool DeleteDC(IntPtr hdc);

    [LibraryImport("gdi32.dll")]
    private static partial int GetDIBits(IntPtr hdc, IntPtr hbmp, uint uStartScan, uint cScanLines,
        byte[] lpvBits, ref BITMAPINFO lpbmi, uint uUsage);

    [LibraryImport("user32.dll")]
    private static partial int GetSystemMetrics(int nIndex);

    [LibraryImport("user32.dll", EntryPoint = "SystemParametersInfoW", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool SystemParametersInfo(int uiAction, int uiParam, ref RECT pvParam, int fWinIni);

    private const int SPI_GETWORKAREA = 0x0030;

    private const int SM_XVIRTUALSCREEN = 76;
    private const int SM_YVIRTUALSCREEN = 77;
    private const int SM_CXVIRTUALSCREEN = 78;
    private const int SM_CYVIRTUALSCREEN = 79;

    [StructLayout(LayoutKind.Sequential)]
    private struct RECT
    {
        public int Left;
        public int Top;
        public int Right;
        public int Bottom;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct BITMAPINFO
    {
        public int biSize;
        public int biWidth;
        public int biHeight;
        public short biPlanes;
        public short biBitCount;
        public int biCompression;
        public int biSizeImage;
        public int biXPelsPerMeter;
        public int biYPelsPerMeter;
        public int biClrUsed;
        public int biClrImportant;
    }

    #endregion Win32 API
}

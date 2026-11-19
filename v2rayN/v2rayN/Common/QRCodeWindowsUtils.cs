using System.Drawing;
using System.Windows.Media;
using System.Windows.Media.Imaging;

namespace v2rayN.Common;

public class QRCodeWindowsUtils
{
    public static ImageSource? GetQRCode(string? strContent)
    {
        if (strContent is null)
        {
            return null;
        }
        try
        {
            var qrCodeImage = QRCodeUtils.GenQRCode(strContent);
            return qrCodeImage is null ? null : ByteToImage(qrCodeImage);
        }
        catch (Exception ex)
        {
            Logging.SaveLog("GetQRCode", ex);
            return null;
        }
    }

    /// <summary>
    /// Captures the entire virtual screen (all monitors) so a QR code can be
    /// displayed anywhere on any display. The virtual-screen metrics and
    /// CopyFromScreen share the same coordinate space (which depends on the
    /// process DPI awareness), so the captured rectangle is always correct
    /// regardless of the system DPI scaling or monitor layout.
    /// </summary>
    public static byte[]? CaptureScreen(Window window)
    {
        try
        {
            int x = GetSystemMetrics(SM_XVIRTUALSCREEN);
            int y = GetSystemMetrics(SM_YVIRTUALSCREEN);
            int width = GetSystemMetrics(SM_CXVIRTUALSCREEN);
            int height = GetSystemMetrics(SM_CYVIRTUALSCREEN);

            if (width <= 0 || height <= 0)
            {
                // Fallback to the primary work area.
                x = (int)SystemParameters.WorkArea.Left;
                y = (int)SystemParameters.WorkArea.Top;
                width = (int)SystemParameters.WorkArea.Width;
                height = (int)SystemParameters.WorkArea.Height;
            }

            using var fullImage = new Bitmap(width, height);
            using var g = Graphics.FromImage(fullImage);

            g.CopyFromScreen(x, y, 0, 0, fullImage.Size, CopyPixelOperation.SourceCopy);
            return ImageToByte(fullImage);
        }
        catch (Exception ex)
        {
            Logging.SaveLog("CaptureScreen", ex);
            return null;
        }
    }

    private const int SM_XVIRTUALSCREEN = 76;
    private const int SM_YVIRTUALSCREEN = 77;
    private const int SM_CXVIRTUALSCREEN = 78;
    private const int SM_CYVIRTUALSCREEN = 79;

    [DllImport("user32.dll")]
    private static extern int GetSystemMetrics(int nIndex);

    private static ImageSource? ByteToImage(IEnumerable imageData)
    {
        return new ImageSourceConverter().ConvertFrom(imageData) as BitmapSource;
    }

    private static byte[]? ImageToByte(Image img)
    {
        return new ImageConverter().ConvertTo(img, typeof(byte[])) as byte[];
    }
}

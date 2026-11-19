using Avalonia.Data.Converters;
using Avalonia.Media;

namespace v2rayN.Desktop.Converters;

/// <summary>
/// Maps a warning level (used by the expiry and traffic/limit columns) to a
/// cell background: 2 = red (expired / quota almost used up), 1 = yellow
/// (expires within a month / remaining below 20%), 0 = default (transparent).
/// </summary>
public class WarningLevelBackgroundConverter : IValueConverter
{
    public object? Convert(object? value, Type targetType, object? parameter, CultureInfo culture)
    {
        return value switch
        {
            2 => new SolidColorBrush(Color.Parse("#E53935")),
            1 => new SolidColorBrush(Color.Parse("#FFD600")),
            _ => Brushes.Transparent,
        };
    }

    public object? ConvertBack(object? value, Type targetType, object? parameter, CultureInfo culture)
    {
        return null;
    }
}

/// <summary>
/// Maps a warning level to a readable text foreground on the warning
/// background: 2 = white (on red), 1 = black (on yellow), 0 = default.
/// </summary>
public class WarningLevelForegroundConverter : IValueConverter
{
    public object? Convert(object? value, Type targetType, object? parameter, CultureInfo culture)
    {
        return value switch
        {
            2 => Brushes.White,
            1 => Brushes.Black,
            _ => null,
        };
    }

    public object? ConvertBack(object? value, Type targetType, object? parameter, CultureInfo culture)
    {
        return null;
    }
}

namespace ServiceLib.Services;

/// <summary>
/// Queries the s-ui ZY4 account query service (sub/queryService.go) for a node's
/// account expiry. The request is sent to the node's own address on a fixed port:
///   http://{node.Address}:{AccountQueryPort}{AccountQueryPath}?key={node.Password}
/// It tries via the local SOCKS proxy first (GFW-safe), then falls back to direct.
/// </summary>
public static class AccountQueryService
{
    private static readonly string _tag = "AccountQueryService";

    /// <summary>Remaining traffic ratio below which the traffic column turns yellow.</summary>
    private const double TrafficWarningRatio = 0.20;

    /// <summary>Remaining traffic ratio at or below which the traffic column turns red.</summary>
    private const double TrafficCriticalRatio = 0.01;

    /// <summary>
    /// Queries a single node and returns its account data, or null on any failure.
    /// </summary>
    public static async Task<AccountQueryData?> QueryNodeAsync(ProfileItem node)
    {
        if (node is null || node.Address.IsNullOrEmpty() || node.Password.IsNullOrEmpty())
        {
            return null;
        }

        var url = $"http://{node.Address}:{Global.AccountQueryPort}{Global.AccountQueryPath}?key={Utils.UrlEncode(node.Password)}";

        string? json = null;

        // Try via the local SOCKS proxy first (if it is running).
        var port = AppManager.Instance.GetLocalPort(EInboundProtocol.socks);
        if (await SocketCheck(Global.Loopback, port))
        {
            json = await TryGetAsync(url, new WebProxy($"socks5://{Global.Loopback}:{port}"));
        }

        // Fall back to a direct connection.
        if (json is null)
        {
            json = await TryGetAsync(url, null);
        }

        if (json.IsNullOrEmpty())
        {
            return null;
        }

        var response = JsonUtils.Deserialize<AccountQueryResponse>(json);
        if (response is null || response.code != 200 || response.data is null)
        {
            return null;
        }

        return response.data;
    }

    /// <summary>
    /// Formats the expiry for display. Returns "-" when the account never expires
    /// or is unknown, the readable expiry date otherwise.
    /// </summary>
    public static string? FormatExpireDate(AccountQueryData? data)
    {
        if (data is null)
        {
            return null;
        }

        if (data.unlimited_time)
        {
            return "-";
        }

        if (!data.expiry_str.IsNullOrEmpty())
        {
            // Show only the date portion (e.g. "2027-10-19 03:29:59" -> "2027-10-19").
            var parts = data.expiry_str.Split(' ');
            return parts.Length > 0 ? parts[0] : data.expiry_str;
        }

        if (data.expiry > 0)
        {
            try
            {
                return DateTimeOffset.FromUnixTimeSeconds(data.expiry).LocalDateTime.ToString("yyyy-MM-dd");
            }
            catch
            {
                return null;
            }
        }

        return null;
    }

    /// <summary>
    /// Formats the traffic usage / limit for display. When the account has a
    /// traffic limit, returns "used / limit"; when there is no limit (unlimited
    /// traffic or no quota), returns "-".
    /// </summary>
    public static string FormatTraffic(AccountQueryData? data)
    {
        if (data is null)
        {
            return "-";
        }

        if (data.unlimited_traffic || data.volume <= 0)
        {
            return "-";
        }

        var used = !data.used_str.IsNullOrEmpty() ? data.used_str : Utils.HumanFy(data.used);
        var limit = !data.volume_str.IsNullOrEmpty() ? data.volume_str : Utils.HumanFy(data.volume);
        return $"{used} / {limit}";
    }

    /// <summary>
    /// True when the account expiry time is already in the past.
    /// </summary>
    public static bool IsAlreadyExpired(long expireTime)
    {
        if (expireTime <= 0)
        {
            return false;
        }

        try
        {
            return DateTimeOffset.FromUnixTimeSeconds(expireTime) < DateTimeOffset.Now;
        }
        catch
        {
            return false;
        }
    }

    /// <summary>
    /// Warning level for the expiry column:
    ///   2 = expires today or already expired (red);
    ///   1 = expires within one month (yellow);
    ///   0 = no expiry (unlimited / unknown) or more than one month away (default).
    /// </summary>
    public static int GetExpireLevel(long expireTime)
    {
        if (expireTime <= 0)
        {
            return 0;
        }

        try
        {
            var expiry = DateTimeOffset.FromUnixTimeSeconds(expireTime).LocalDateTime;
            var endOfToday = DateTime.Now.Date.AddDays(1).AddTicks(-1);
            if (expiry <= endOfToday)
            {
                return 2;
            }

            return expiry <= DateTimeOffset.Now.AddMonths(1).LocalDateTime ? 1 : 0;
        }
        catch
        {
            return 0;
        }
    }

    /// <summary>
    /// Warning level for the traffic/limit column, based on the remaining
    /// traffic ratio (remaining / limit):
    ///   2 = remaining ratio &lt;= 1% (red);
    ///   1 = remaining ratio &lt; 20% (yellow);
    ///   0 = no limit / unlimited traffic or plenty left (default).
    /// </summary>
    public static int GetTrafficLevel(long volume, long used)
    {
        if (volume <= 0)
        {
            return 0;
        }

        var remainingRatio = (double)(volume - used) / volume;
        if (remainingRatio <= TrafficCriticalRatio)
        {
            return 2;
        }

        return remainingRatio < TrafficWarningRatio ? 1 : 0;
    }

    private static async Task<string?> TryGetAsync(string url, IWebProxy? proxy)
    {
        try
        {
            var handler = new SocketsHttpHandler
            {
                Proxy = proxy,
                UseProxy = proxy != null,
                ConnectTimeout = TimeSpan.FromSeconds(2)
            };
            using var client = new HttpClient(handler)
            {
                Timeout = TimeSpan.FromSeconds(Global.AccountQueryTimeout)
            };
            var response = await client.GetAsync(url);
            return await response.Content.ReadAsStringAsync();
        }
        catch (Exception ex)
        {
            Logging.SaveLog(_tag, ex);
            return null;
        }
    }

    private static async Task<bool> SocketCheck(string ip, int port)
    {
        try
        {
            IPEndPoint point = new(IPAddress.Parse(ip), port);
            using Socket? sock = new(AddressFamily.InterNetwork, SocketType.Stream, ProtocolType.Tcp);
            await sock.ConnectAsync(point);
            return true;
        }
        catch
        {
            return false;
        }
    }
}

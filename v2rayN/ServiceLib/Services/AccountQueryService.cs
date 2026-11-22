namespace ServiceLib.Services;

/// <summary>
/// Queries the s-ui ZY4 account query service (sub/queryService.go) for a node's
/// account expiry. The request is sent to the node's own address on a fixed port:
///   http://{node.Address}:{AccountQueryPort}{AccountQueryPath}?key={node.Password}
/// It tries via the local SOCKS proxy first (GFW-safe), then falls back to direct.
/// zhdata nodes cannot be reached that way: the core tunnels everything and may sit behind
/// several transparent forwarding hops, so their query is sent to a reserved hostname through
/// the local zhdata SOCKS port and carried inside the zhdata data channel.
/// </summary>
public static class AccountQueryService
{
    private static readonly string _tag = "AccountQueryService";

    /// <summary>Remaining traffic ratio below which the traffic column turns yellow.</summary>
    private const double TrafficWarningRatio = 0.20;

    /// <summary>Remaining traffic ratio at or below which the traffic column turns red.</summary>
    private const double TrafficCriticalRatio = 0.01;

    // One retry after this pause for a query stream that a cold tunnel dropped.
    private static readonly TimeSpan QueryRetryDelay = TimeSpan.FromMilliseconds(400);

    /// <summary>
    /// Queries a single node and returns its account data, or null on any failure.
    /// </summary>
    public static async Task<AccountQueryData?> QueryNodeAsync(ProfileItem node)
    {
        if (node is null || node.Address.IsNullOrEmpty() || node.Password.IsNullOrEmpty())
        {
            return null;
        }

        var json = node.ConfigType == EConfigType.ZhData
            ? await QueryViaZhDataTunnelAsync(node)
            : await QueryViaPanelAsync(node);

        return ParseAccountJson(json);
    }

    /// <summary>
    /// Plain panel query: the node answers on its own address, on the fixed query port.
    /// </summary>
    private static async Task<string?> QueryViaPanelAsync(ProfileItem node)
    {
        var url = $"http://{node.Address}:{Global.AccountQueryPort}{Global.AccountQueryPath}?key={Utils.UrlEncode(node.Password)}";

        // Try via the local SOCKS proxy first (if it is running).
        var port = AppManager.Instance.GetLocalPort(EInboundProtocol.socks);
        if (await SocketCheck(Global.Loopback, port))
        {
            var json = await TryGetAsync(url, new WebProxy($"socks5://{Global.Loopback}:{port}"));
            if (json.IsNotEmpty())
            {
                return json;
            }
        }

        // Fall back to a direct connection.
        return await TryGetAsync(url, null);
    }

    /// <summary>
    /// zhdata query: a reserved hostname is requested through the local zhdata SOCKS port, which
    /// the core recognizes and turns into a query carried by its own data channel. That hostname
    /// is only understood on this path, so there is no direct fallback here.
    /// </summary>
    private static async Task<string?> QueryViaZhDataTunnelAsync(ProfileItem node)
    {
        var url = $"http://{Global.AccountQueryZhDataHost}{Global.AccountQueryPath}?key={Utils.UrlEncode(node.Password)}";

        var port = GetZhDataListenPort(node);
        if (port <= 0 || !await SocketCheck(Global.Loopback, port))
        {
            return null;
        }

        var proxy = new WebProxy($"socks5://{Global.Loopback}:{port}");
        var json = await TryGetAsync(url, proxy);
        if (json.IsNullOrEmpty())
        {
            // A cold or freshly reconnected tunnel can tear the first stream down before the
            // request lands, which reads as an empty answer. One retry covers that; a real
            // "not found" answer is JSON and never retried here.
            await Task.Delay(QueryRetryDelay);
            json = await TryGetAsync(url, proxy);
        }

        return json;
    }

    /// <summary>
    /// The port the running zhdata core listens on: it keeps the local socks port unless a routing
    /// core fronts it, in which case it was moved by the chained core offset.
    /// </summary>
    private static int GetZhDataListenPort(ProfileItem node)
    {
        var config = AppManager.Instance.Config;
        var coreType = AppManager.Instance.GetCoreType(node, node.ConfigType);
        if (ConfigHandler.NeedsRoutingFrontCore(node, coreType) && ConfigHandler.GetRoutingFrontCoreType() != null)
        {
            return ConfigHandler.GetZhDataChainPort(config);
        }

        return AppManager.Instance.GetLocalPort(EInboundProtocol.socks);
    }

    private static AccountQueryData? ParseAccountJson(string? json)
    {
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

        if (data.expiry > 0)
        {
            try
            {
                // The unix timestamp is authoritative: a core answers in its own timezone, so the
                // text form (expiry_str) could name another day.
                return DateTimeOffset.FromUnixTimeSeconds(data.expiry).LocalDateTime.ToString("yyyy-MM-dd");
            }
            catch
            {
                return null;
            }
        }

        if (!data.expiry_str.IsNullOrEmpty())
        {
            // Show only the date portion (e.g. "2027-10-19 03:29:59" -> "2027-10-19").
            var parts = data.expiry_str.Split(' ');
            return parts.Length > 0 ? parts[0] : data.expiry_str;
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

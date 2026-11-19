namespace ServiceLib.Handler.Fmt;

public class ZhDataFmt : BaseFmt
{
    public static ProfileItem? Resolve(string str, out string msg)
    {
        msg = ResUI.ConfigurationFormatIncorrect;

        var item = ResolveZhData(str);
        if (item == null || item.Address.IsNullOrEmpty() || item.Port <= 0 || item.Password.IsNullOrEmpty())
        {
            return null;
        }

        item.ConfigType = EConfigType.ZhData;
        return item;
    }

    public static string? ToUri(ProfileItem? item)
    {
        if (item == null)
        {
            return null;
        }

        var extra = ParseExtra(item);
        var servers = GetUserServers(item, extra);
        if (servers.Count == 0)
        {
            return null;
        }

        var query = new List<string>();
        if (servers.Count > 1)
        {
            query.Add($"servers={Utils.UrlEncode(string.Join(",", servers.Select(s => GetUriServer(s))))}");
        }
        if (extra.Conns != ZhDataExtraItem.DefaultConns)
        {
            query.Add($"conns={extra.Conns}");
        }
        if (extra.Spread != ZhDataExtraItem.DefaultSpread)
        {
            query.Add($"spread={extra.Spread}");
        }
        if (extra.DlConns != ZhDataExtraItem.DefaultDlConns)
        {
            query.Add($"dlconns={extra.DlConns}");
        }
        if (extra.Migrate != ZhDataExtraItem.DefaultMigrate)
        {
            query.Add($"migrate={extra.Migrate}");
        }
        if (extra.SlotBase > 0)
        {
            query.Add($"slotbase={extra.SlotBase}");
        }
        if (extra.Verbose)
        {
            query.Add("verbose=1");
        }
        if (!extra.AutoPorts)
        {
            query.Add("autoports=0");
        }
        if (extra.PortSpan != ZhDataExtraItem.DefaultPortSpan)
        {
            query.Add($"portspan={extra.PortSpan}");
        }

        var remark = string.Empty;
        if (item.Remarks.IsNotEmpty())
        {
            remark = "#" + Utils.UrlEncode(item.Remarks);
        }

        var queryText = query.Count > 0 ? "?" + string.Join("&", query) : string.Empty;
        return $"{Global.ProtocolShares[EConfigType.ZhData]}{Utils.UrlEncode(item.Password)}@{GetUriServer(servers.First())}{queryText}{remark}";
    }

    public static ZhDataExtraItem ParseExtra(ProfileItem? item)
    {
        var extra = JsonUtils.Deserialize<ZhDataExtraItem>(item?.ProtoExtra);
        if (extra == null)
        {
            extra = new ZhDataExtraItem();
        }

        if (extra.Servers.Count == 0 && item != null)
        {
            extra.Servers.AddRange(ParseAddressServers(item));
        }

        return extra;
    }

    public static List<string> GetUserServers(ProfileItem? item, ZhDataExtraItem? extra = null)
    {
        extra ??= ParseExtra(item);
        var servers = extra.Servers
            .Select(NormalizeServer)
            .Where(s => s.IsNotEmpty())
            .Select(s => s!)
            .Distinct()
            .ToList();

        if (servers.Count == 0 && item != null)
        {
            servers.AddRange(ParseAddressServers(item));
        }

        return servers.Distinct().ToList();
    }

    public static List<string> GetServers(ProfileItem? item, ZhDataExtraItem? extra = null)
    {
        extra ??= ParseExtra(item);
        return ExpandAutoPorts(GetUserServers(item, extra), extra);
    }

    private static List<string> ExpandAutoPorts(List<string> servers, ZhDataExtraItem extra)
    {
        if (!extra.AutoPorts || extra.PortSpan <= 1 || servers.Count != 1)
        {
            return servers;
        }

        if (!TrySplitServer(servers.First(), out var host, out var basePort))
        {
            return servers;
        }

        var expanded = new List<string>();
        for (var offset = 0; offset < extra.PortSpan; offset++)
        {
            var port = basePort + offset;
            if (port > 65535)
            {
                break;
            }

            expanded.Add($"{host}:{port}");
        }

        return expanded.Distinct().ToList();
    }

    private static ProfileItem? ResolveZhData(string result)
    {
        var prefix = Global.ProtocolShares[EConfigType.ZhData];
        if (!result.StartsWith(prefix))
        {
            return null;
        }

        result = result[prefix.Length..];

        var item = new ProfileItem
        {
            ConfigType = EConfigType.ZhData
        };

        var indexRemark = result.IndexOf('#');
        if (indexRemark > 0)
        {
            try
            {
                item.Remarks = Utils.UrlDecode(result[(indexRemark + 1)..]);
            }
            catch
            {
            }
            result = result[..indexRemark];
        }

        var indexQuery = result.IndexOf('?');
        var queryText = string.Empty;
        if (indexQuery > 0)
        {
            queryText = result[(indexQuery + 1)..];
            result = result[..indexQuery];
        }

        var indexAt = result.LastIndexOf('@');
        if (indexAt <= 0 || indexAt == result.Length - 1)
        {
            return null;
        }

        item.Password = Utils.UrlDecode(result[..indexAt]);
        var authority = result[(indexAt + 1)..];
        var servers = authority
            .Split(',', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries)
            .Select(NormalizeServer)
            .Where(s => s.IsNotEmpty())
            .Select(s => s!)
            .Distinct()
            .ToList();

        var extra = new ZhDataExtraItem();
        if (queryText.IsNotEmpty())
        {
            var query = Utils.ParseQueryString(queryText);
            extra.Conns = GetQueryInt(query, "conns", extra.Conns);
            extra.Spread = GetQueryInt(query, "spread", extra.Spread);
            extra.DlConns = GetQueryInt(query, "dlconns", extra.DlConns);
            extra.Migrate = GetQueryInt(query, "migrate", extra.Migrate);
            extra.SlotBase = GetQueryInt(query, "slotbase", extra.SlotBase);
            extra.Verbose = GetQueryBool(query, "verbose") || GetQueryBool(query, "v");
            extra.AutoPorts = !GetQueryBool(query, "noautoports") && GetQueryBoolOrDefault(query, "autoports", extra.AutoPorts);
            extra.PortSpan = GetQueryInt(query, "portspan", extra.PortSpan);

            var queryServers = GetQueryValue(query, "servers")
                .Split(',', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries)
                .Select(NormalizeServer)
                .Where(s => s.IsNotEmpty())
                .Select(s => s!)
                .ToList();
            if (queryServers.Count > 0)
            {
                servers = queryServers;
            }
        }

        if (servers.Count == 0)
        {
            return null;
        }

        extra.Servers = servers;
        SetFirstServer(item, servers.First());
        item.ProtoExtra = JsonUtils.Serialize(extra, false);

        return item;
    }

    private static void SetFirstServer(ProfileItem item, string server)
    {
        var index = server.LastIndexOf(':');
        if (index <= 0)
        {
            return;
        }

        var host = server[..index];
        if (host.StartsWith('[') && host.EndsWith(']'))
        {
            host = host[1..^1];
        }

        item.Address = host;
        item.Port = server[(index + 1)..].ToInt();
    }

    public static void ApplyFirstServer(ProfileItem item, string server)
    {
        SetFirstServer(item, server);
    }

    public static List<string> ParseAddressServers(ProfileItem? item)
    {
        var servers = new List<string>();
        if (item == null || item.Address.IsNullOrEmpty())
        {
            return servers;
        }

        var raw = item.Address.Trim();
        if (raw.IsNullOrEmpty())
        {
            return servers;
        }

        var parts = raw.Split([',', ';', ' ', '\r', '\n', '\t'], StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);
        foreach (var part in parts)
        {
            var server = part.Contains(':') ? NormalizeServer(part) : NormalizeServer($"{part}:{item.Port}");
            if (server.IsNotEmpty())
            {
                servers.Add(server);
            }
        }

        return servers.Distinct().ToList();
    }

    private static string NormalizeServer(string server)
    {
        if (!TrySplitServer(server, out var host, out var port))
        {
            return string.Empty;
        }

        return $"{host}:{port}";
    }

    private static bool TrySplitServer(string server, out string host, out int port)
    {
        host = string.Empty;
        port = 0;

        if (server.IsNullOrEmpty())
        {
            return false;
        }

        var value = server.Trim();
        var index = value.LastIndexOf(':');
        if (index <= 0 || index == value.Length - 1)
        {
            return false;
        }

        var hostPart = value[..index].Trim();
        var portText = value[(index + 1)..].Trim();
        if (hostPart.IsNullOrEmpty() || !int.TryParse(portText, out var parsedPort) || parsedPort <= 0 || parsedPort > 65535)
        {
            return false;
        }

        if (hostPart.StartsWith('[') && hostPart.EndsWith(']'))
        {
            host = hostPart;
        }
        else if (hostPart.Count(':') > 1)
        {
            host = $"[{hostPart}]";
        }
        else
        {
            host = hostPart;
        }

        port = parsedPort;
        return true;
    }

    private static string GetServer(string address, int port)
    {
        if (address.IsNullOrEmpty() || port <= 0)
        {
            return string.Empty;
        }

        return NormalizeServer($"{address}:{port}");
    }

    private static string GetUriServer(string server)
    {
        if (!TrySplitServer(server, out var host, out var port))
        {
            return server;
        }

        if (host.StartsWith('[') && host.EndsWith(']'))
        {
            host = host[1..^1];
        }

        return $"{GetIpv6(host)}:{port}";
    }

    private static int GetQueryInt(NameValueCollection query, string key, int defaultValue)
    {
        var value = GetQueryValue(query, key);
        return int.TryParse(value, out var result) ? result : defaultValue;
    }

    private static bool GetQueryBool(NameValueCollection query, string key)
    {
        var value = GetQueryValue(query, key).ToLowerInvariant();
        return value is "1" or "true" or "yes" or "on";
    }

    private static bool GetQueryBoolOrDefault(NameValueCollection query, string key, bool defaultValue)
    {
        var value = GetQueryValue(query, key).ToLowerInvariant();
        return value switch
        {
            "" => defaultValue,
            "1" or "true" or "yes" or "on" => true,
            "0" or "false" or "no" or "off" => false,
            _ => defaultValue,
        };
    }
}

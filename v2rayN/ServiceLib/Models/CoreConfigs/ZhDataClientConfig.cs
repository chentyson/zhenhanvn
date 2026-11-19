namespace ServiceLib.Models.CoreConfigs;

public class ZhDataClientConfig
{
    [JsonPropertyName("mode")]
    public string Mode { get; set; } = "client";

    [JsonPropertyName("listen")]
    public string Listen { get; set; } = string.Empty;

    [JsonPropertyName("server")]
    public string Server { get; set; } = string.Empty;

    [JsonPropertyName("servers")]
    public List<string> Servers { get; set; } = [];

    [JsonPropertyName("uuid")]
    public string Uuid { get; set; } = string.Empty;

    [JsonPropertyName("conns")]
    public int Conns { get; set; } = 1;

    [JsonPropertyName("spread")]
    public int Spread { get; set; } = 8;

    [JsonPropertyName("dlconns")]
    public int DlConns { get; set; } = -1;

    [JsonPropertyName("migrate")]
    public int Migrate { get; set; } = 1;

    [JsonPropertyName("slotbase")]
    public int SlotBase { get; set; }

    [JsonPropertyName("verbose")]
    public bool Verbose { get; set; }
}

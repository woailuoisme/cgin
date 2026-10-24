package config

// GeoIPConfig 描述基于 ip2region 的离线 IP 归属地库配置。
type GeoIPConfig struct {
	Enabled bool   `env:"GEOIP_ENABLED"`
	DBPath  string `env:"GEOIP_DB_PATH" envDefault:"./storage/app/geoip/ip2region_v4.xdb"`
}

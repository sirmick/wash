package radio

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	_ "embed"
)

const maxStationConfigBytes = 512 * 1024

//go:embed default-stations.json
var defaultStationsJSON []byte

type stationConfigFile struct {
	Version  int       `json:"version,omitempty"`
	Stations []station `json:"stations"`
}

func configuredStations() []station {
	stations, err := loadStationConfig()
	if err == nil {
		return stations
	}
	log.Printf("wash-radio: load station config: %v (using embedded defaults)", err)
	stations, err = decodeStationConfig(defaultStationsJSON)
	if err != nil {
		log.Printf("wash-radio: embedded station defaults are invalid: %v", err)
		return nil
	}
	return stations
}

func loadStationConfig() ([]station, error) {
	path := stationConfigPath()
	if path == "" {
		return nil, errors.New("no config dir (HOME/XDG_CONFIG_HOME unset)")
	}
	if err := seedStationConfig(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > maxStationConfigBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxStationConfigBytes)
	}
	return decodeStationConfig(data)
}

func seedStationConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeStationConfig(path, defaultStationsJSON)
}

func stationConfigDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "wash")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "wash")
	}
	return ""
}

func stationConfigPath() string {
	d := stationConfigDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "radio-stations.json")
}

func writeStationConfig(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".radio-stations-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func decodeStationConfig(data []byte) ([]station, error) {
	var cfg stationConfigFile
	if err := json.Unmarshal(data, &cfg); err == nil && cfg.Stations != nil {
		return sanitizeStations(cfg.Stations), nil
	}
	var stations []station
	if err := json.Unmarshal(data, &stations); err != nil {
		return nil, err
	}
	return sanitizeStations(stations), nil
}

func sanitizeStations(stations []station) []station {
	out := make([]station, 0, len(stations))
	for _, st := range stations {
		st.Name = strings.TrimSpace(st.Name)
		st.URL = strings.TrimSpace(st.URL)
		st.Codec = strings.TrimSpace(st.Codec)
		st.Genre = strings.TrimSpace(st.Genre)
		st.Subtype = strings.TrimSpace(st.Subtype)
		st.Source = strings.TrimSpace(st.Source)
		st.Description = strings.TrimSpace(st.Description)
		if st.URL == "" {
			continue
		}
		if st.Name == "" {
			st.Name = st.URL
		}
		out = append(out, st)
	}
	return out
}

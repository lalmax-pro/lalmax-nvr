package camera

import (
	"context"
	"log/slog"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// SyncCamerasFromStorage makes the database the source of truth for cameras.
// Legacy cameras listed in YAML are migrated into the DB once, then stripped from the config file.
func SyncCamerasFromStorage(ctx context.Context, cfg *config.Config, db *storage.DB, configPath string) error {
	if cfg == nil || db == nil {
		return nil
	}

	legacy := append([]config.CameraConfig(nil), cfg.Cameras...)
	if len(legacy) > 0 {
		for _, cam := range legacy {
			if err := db.UpsertCamera(ctx, cam.ID, cam.Name, string(cam.Protocol), cam.Encoding, cam.URL, cam.Username, cam.Password, cam.Enabled, cam.ONVIFEndpoint, cam.ProfileToken, cam.StreamEncoding, config.NormalizeRTSPTransport(cam.RTSPTransport)); err != nil {
				return err
			}
			if cam.StreamID == "" {
				cam.StreamID = cam.ID
			}
			if err := db.SaveCameraExtras(ctx, cam); err != nil {
				return err
			}
			if err := db.BindStreamToCamera(ctx, cam.StreamID, cam.ID); err != nil {
				return err
			}
		}
		slog.Info("migrated cameras from YAML config to database", "count", len(legacy))
	}

	loaded, err := db.ListCameraConfigs(ctx)
	if err != nil {
		return err
	}
	cfg.Cameras = loaded

	if len(legacy) > 0 && configPath != "" {
		if err := config.Save(configPath, cfg); err != nil {
			return err
		}
		slog.Info("removed cameras from YAML config file", "path", configPath)
	}
	return nil
}

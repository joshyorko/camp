package config

import "github.com/joshyorko/camp/internal/domain"

func DurableConfiguration(runtime Runtime, backend FileBackend, paths XDGPaths) domain.ConfigurationRecord {
	return domain.ConfigurationRecord{
		Capsule: runtime.Capsule, BackendKind: "file", BackendURL: backend.SanitizedURL, BackendFingerprint: backend.Fingerprint,
		Source: runtime.Source, RegistryPort: runtime.RegistryPort, FileserverPort: runtime.FileserverPort,
		DevcontainerPath: runtime.DevcontainerPath,
		Paths: domain.SessionPaths{
			DataRoot: paths.DataRoot, WorkRoot: paths.WorkRoot, StoreRoot: paths.StoreRoot, SessionRoot: paths.SessionRoot, CacheRoot: paths.CacheRoot,
		},
	}
}

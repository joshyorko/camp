package config

import "github.com/joshyorko/camp/internal/domain"

func DurableConfiguration(runtime Runtime, backend FileBackend, paths XDGPaths) domain.ConfigurationRecord {
	return durableConfiguration(runtime, "file", backend.SanitizedURL, backend.Fingerprint, paths)
}

func DurableBackendConfiguration(runtime Runtime, backend Backend, paths XDGPaths) domain.ConfigurationRecord {
	return durableConfiguration(runtime, string(backend.Kind), backend.SanitizedURL, backend.Fingerprint, paths)
}

func durableConfiguration(runtime Runtime, kind, sanitizedURL, fingerprint string, paths XDGPaths) domain.ConfigurationRecord {
	return domain.ConfigurationRecord{
		Capsule: runtime.Capsule, BackendKind: kind, BackendURL: sanitizedURL, BackendFingerprint: fingerprint,
		Source: runtime.Source, RegistryPort: runtime.RegistryPort, FileserverPort: runtime.FileserverPort,
		DevcontainerPath: runtime.DevcontainerPath,
		Paths: domain.SessionPaths{
			DataRoot: paths.DataRoot, WorkRoot: paths.WorkRoot, StoreRoot: paths.StoreRoot, SessionRoot: paths.SessionRoot, CacheRoot: paths.CacheRoot,
		},
	}
}

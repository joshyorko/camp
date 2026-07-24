# Camp Setup First-Run Experience Design

## Goal

`camp setup` is the obvious first command on a clean Linux machine. It gathers the minimum non-secret configuration when none exists, initializes the capsule, prepares Camp's managed tools, and finishes with the existing real-data animated campsite.

## Interaction

When persistent configuration already exists, setup remains repeatable and immediately verifies or reuses the managed tools before rendering the campsite.

When configuration is absent in human mode, setup prompts in this order:

1. source path, defaulting to the current directory;
2. capsule name, defaulting to the source directory name;
3. backend URL, defaulting to Camp's XDG file backend;
4. DevPod provider, defaulting to `docker`;
5. DevPod context, defaulting to `default`.

Empty answers accept the displayed defaults. EOF or an empty required value fails without writing partial configuration. JSON mode never prompts.

## Output

Normal human output reports verified tool waypoints and the campsite. It does not print managed executable paths, checksums, or a shell `PATH` export because Camp resolves managed tools internally. JSON output retains the complete stable machine-readable setup result.

The full-color redraw remains gated by the existing terminal capability detection. Plain terminals receive the same truthful facts without cursor controls.

## Safety and verification

The wizard delegates validation, capsule initialization, and atomic configuration persistence to the existing configured `init` path. It stores no credentials. Tests must witness the absent-config failure before implementation, prove default and explicit prompt values, prove concise human output and unchanged detailed JSON, and keep existing presentation capability tests green.


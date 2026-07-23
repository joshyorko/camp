# Checkpoints, cleanup, and recovery

A verified checkpoint is immutable archive and pointer evidence published by Camp's checkpoint pipeline. It is not automatically proof that every byte still present only inside a provider workspace was mirrored into that checkpoint.

For a local provider, workspace return is a validated no-op only when the canonical staging root is the provider's canonical workspace-local folder. For a remote provider, Camp must finish the recorded mirror before building a checkpoint. An interrupted or ambiguous mirror remains recovery evidence; Camp allocates a fresh attempt-specific destination instead of treating uncertain bytes as published.

`camp recover <session>` reloads durable journal state, revalidates the session identity, ownership marker, pending transition, and active writer lease, then reconciles only the recorded operation. It fails closed when evidence changed. Never delete Camp state, a materialization, or a provider workspace merely to clear a recovery error.

Close separates publication from cleanup. A published checkpoint is not repeated to repair cleanup. Cleanup acts only on recorded workspace, process, lease, and owned-path identities; adopted source roots are never Camp cleanup targets. When recovery cannot prove the exact next action, preserve the workspace and journal and diagnose before retrying.

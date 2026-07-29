import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, readdir, rename, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { applyMetadata, writeLockfilesAtomically } from "./update_dependency_locks.mjs";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");

const tools = `schemaVersion: 1
tools:
  devpod:
    repository: skevetter/devpod
    version: v1.2.3
    commit: 1111111111111111111111111111111111111111
    assets:
      linux:
        amd64:
          url: https://old/devpod-amd64
          sha256: ${"1".repeat(64)}
        arm64:
          url: https://old/devpod-arm64
          sha256: ${"2".repeat(64)}
  hauler:
    repository: hauler-dev/hauler
    version: v2.3.4
    commit: 2222222222222222222222222222222222222222
    assets:
      linux:
        amd64:
          url: https://old/hauler-amd64
          sha256: ${"3".repeat(64)}
        arm64:
          url: https://old/hauler-arm64
          sha256: ${"4".repeat(64)}
fixtures:
  room:
    repository: joshyorko/room-of-requirement
    version: v3.4.5
    commit: 3333333333333333333333333333333333333333
  lifecycle:
    image: quay.io/podman/stable@sha256:${"5".repeat(64)}
`;

const rcc = `schemaVersion: 1
version: v4.5.6
commit: 4444444444444444444444444444444444444444
host: linux/amd64
name: rcc-linux64
url: https://old/rcc
sha256: ${"6".repeat(64)}
`;

const asset = (name, digit) => ({
  url: `https://github.com/example/releases/download/v9/${name}`,
  sha256: digit.repeat(64)
});

test("applyMetadata refreshes every version-coupled lock field", () => {
  const updated = applyMetadata(tools, rcc, {
    devpod: {
      commit: "a".repeat(40),
      assets: { amd64: asset("devpod-amd64", "a"), arm64: asset("devpod-arm64", "b") }
    },
    hauler: {
      commit: "b".repeat(40),
      assets: { amd64: asset("hauler-amd64", "c"), arm64: asset("hauler-arm64", "d") }
    },
    room: { commit: "c".repeat(40), assets: {} },
    rcc: { commit: "d".repeat(40), assets: { linux64: asset("rcc-linux64", "e") } }
  });

  for (const stale of [
    "1111111111111111111111111111111111111111",
    "2222222222222222222222222222222222222222",
    "3333333333333333333333333333333333333333",
    "4444444444444444444444444444444444444444",
    "https://old/",
    "1".repeat(64),
    "2".repeat(64),
    "3".repeat(64),
    "4".repeat(64),
    "6".repeat(64)
  ]) {
    assert.equal(`${updated.tools}\n${updated.rcc}`.includes(stale), false, `stale value remains: ${stale}`);
  }
  assert.match(updated.tools, /version: v1\.2\.3/);
  assert.match(updated.tools, /image: quay\.io\/podman\/stable@sha256:5{64}/);
  assert.match(updated.rcc, /version: v4\.5\.6/);
  assert.match(updated.rcc, new RegExp(`sha256: ${"e".repeat(64)}`));
});

test("applyMetadata fails closed when a coupled field is absent", () => {
  assert.throws(
    () => applyMetadata(tools.replace("          sha256: " + "1".repeat(64) + "\n", ""), rcc, {
      devpod: {
        commit: "a".repeat(40),
        assets: { amd64: asset("devpod-amd64", "a"), arm64: asset("devpod-arm64", "b") }
      },
      hauler: {
        commit: "b".repeat(40),
        assets: { amd64: asset("hauler-amd64", "c"), arm64: asset("hauler-arm64", "d") }
      },
      room: { commit: "c".repeat(40), assets: {} },
      rcc: { commit: "d".repeat(40), assets: { linux64: asset("rcc-linux64", "e") } }
    }),
    /expected one DevPod amd64 SHA-256, found 0/
  );
});

test("Renovate custom managers match every current Camp lock", async () => {
  const config = JSON.parse(await readFile(join(root, "renovate.json"), "utf8"));
  const files = {
    "tools.lock.yaml": await readFile(join(root, "tools.lock.yaml"), "utf8"),
    "developer/rcc.lock.yaml": await readFile(join(root, "developer", "rcc.lock.yaml"), "utf8")
  };
  const found = new Set();
  for (const manager of config.customManagers) {
    const file = Object.entries(files).find(([name]) =>
      manager.managerFilePatterns.some((pattern) =>
        new RegExp(pattern.slice(1, -1)).test(name)
      )
    );
    assert.ok(file, `no lockfile matched ${manager.managerFilePatterns}`);
    const match = new RegExp(manager.matchStrings[0]).exec(file[1]);
    assert.ok(match, `manager for ${manager.depNameTemplate || "lifecycle image"} did not match`);
    if (manager.depNameTemplate) {
      assert.match(match.groups.currentValue, /^v\d+\.\d+\.\d+$/);
      found.add(manager.depNameTemplate);
    } else {
      assert.equal(match.groups.depName, "quay.io/podman/stable");
      assert.match(match.groups.currentDigest, /^sha256:[0-9a-f]{64}$/);
      found.add("quay.io/podman/stable");
    }
  }
  assert.deepEqual(found, new Set([
    "skevetter/devpod",
    "hauler-dev/hauler",
    "joshyorko/room-of-requirement",
    "joshyorko/rcc",
    "quay.io/podman/stable"
  ]));
});

test("two-lock update rolls back when the second install rename fails", async () => {
  const temporaryRoot = await mkdtemp(join(tmpdir(), "camp-renovate-locks-"));
  const toolsPath = join(temporaryRoot, "tools.lock.yaml");
  const developer = join(temporaryRoot, "developer");
  const rccPath = join(developer, "rcc.lock.yaml");
  await mkdir(developer);
  await writeFile(toolsPath, "old tools\n");
  await writeFile(rccPath, "old rcc\n");

  const injectedFS = {
    writeFile,
    rm,
    rename: async (source, destination) => {
      if (source.endsWith(".next") && destination === rccPath) {
        throw new Error("injected second install failure");
      }
      return rename(source, destination);
    }
  };

  try {
    await assert.rejects(
      writeLockfilesAtomically([
        { path: toolsPath, contents: "new tools\n" },
        { path: rccPath, contents: "new rcc\n" }
      ], injectedFS),
      /injected second install failure/
    );
    assert.equal(await readFile(toolsPath, "utf8"), "old tools\n");
    assert.equal(await readFile(rccPath, "utf8"), "old rcc\n");
    assert.deepEqual((await readdir(temporaryRoot)).sort(), ["developer", "tools.lock.yaml"]);
    assert.deepEqual((await readdir(developer)).sort(), ["rcc.lock.yaml"]);
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
});

#!/usr/bin/env node

import { createHash, randomUUID } from "node:crypto";
import { readFile, rename, rm, writeFile } from "node:fs/promises";
import { basename, dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");

function replaceOne(text, pattern, replacement, description) {
  const matches = [...text.matchAll(new RegExp(pattern.source, pattern.flags.includes("g") ? pattern.flags : `${pattern.flags}g`))];
  if (matches.length !== 1) {
    throw new Error(`expected one ${description}, found ${matches.length}`);
  }
  return text.replace(pattern, replacement);
}

function rewriteBlock(text, start, end, rewrite, description) {
  const startIndex = text.indexOf(start);
  if (startIndex < 0) {
    throw new Error(`missing ${description} block`);
  }
  const endIndex = end === null ? text.length : text.indexOf(end, startIndex + start.length);
  if (endIndex < 0) {
    throw new Error(`unterminated ${description} block`);
  }
  return text.slice(0, startIndex) + rewrite(text.slice(startIndex, endIndex)) + text.slice(endIndex);
}

function field(block, name) {
  const match = block.match(new RegExp(`^\\s*${name}:\\s*(\\S+)\\s*$`, "m"));
  if (!match) {
    throw new Error(`missing ${name}`);
  }
  return match[1];
}

async function githubJSON(path, fetchImpl) {
  const headers = {
    Accept: "application/vnd.github+json",
    "User-Agent": "camp-lock-refresh",
    "X-GitHub-Api-Version": "2022-11-28"
  };
  const token = process.env.GITHUB_TOKEN || process.env.RENOVATE_TOKEN;
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  const response = await fetchImpl(`https://api.github.com${path}`, { headers });
  if (!response.ok) {
    throw new Error(`GitHub API ${path} returned ${response.status}`);
  }
  return response.json();
}

async function releaseMetadata(repository, version, assetNames, fetchImpl = fetch) {
  const release = await githubJSON(
    `/repos/${repository}/releases/tags/${encodeURIComponent(version)}`,
    fetchImpl
  );
  let ref = await githubJSON(
    `/repos/${repository}/git/ref/tags/${encodeURIComponent(version)}`,
    fetchImpl
  );
  let object = ref.object;
  for (let depth = 0; object.type === "tag" && depth < 8; depth += 1) {
    object = await githubJSON(
      `/repos/${repository}/git/tags/${object.sha}`,
      fetchImpl
    ).then((tag) => tag.object);
  }
  if (object.type !== "commit" || !/^[0-9a-f]{40}$/.test(object.sha)) {
    throw new Error(`${repository} ${version} does not resolve to one commit`);
  }

  const assets = {};
  for (const name of assetNames) {
    const asset = release.assets.find((candidate) => candidate.name === name);
    if (!asset) {
      throw new Error(`${repository} ${version} is missing release asset ${name}`);
    }
    const response = await fetchImpl(asset.browser_download_url, {
      headers: { "User-Agent": "camp-lock-refresh" },
      redirect: "follow"
    });
    if (!response.ok) {
      throw new Error(`${repository} ${version} asset ${name} returned ${response.status}`);
    }
    const maximumAssetBytes = 1024 * 1024 * 1024;
    const advertisedSize = Number(response.headers.get("content-length") || 0);
    if (advertisedSize > maximumAssetBytes) {
      throw new Error(`${repository} ${version} asset ${name} exceeds 1 GiB`);
    }
    const hash = createHash("sha256");
    let received = 0;
    for await (const chunk of response.body) {
      received += chunk.length;
      if (received > maximumAssetBytes) {
        throw new Error(`${repository} ${version} asset ${name} exceeds 1 GiB`);
      }
      hash.update(chunk);
    }
    assets[name] = {
      url: asset.browser_download_url,
      sha256: hash.digest("hex")
    };
  }
  return { commit: object.sha, assets };
}

export function applyMetadata(toolsText, rccText, metadata) {
  const rewriteTool = (text, start, end, name, values) =>
    rewriteBlock(text, start, end, (block) => {
      let updated = replaceOne(
        block,
        /^    commit: \S+$/m,
        `    commit: ${values.commit}`,
        `${name} commit`
      );
      for (const [architecture, asset] of Object.entries(values.assets)) {
        updated = rewriteBlock(
          updated,
          `        ${architecture}:\n`,
          architecture === "amd64" ? "        arm64:\n" : null,
          (architectureBlock) => {
            let result = replaceOne(
              architectureBlock,
              /^          url: \S+$/m,
              `          url: ${asset.url}`,
              `${name} ${architecture} URL`
            );
            result = replaceOne(
              result,
              /^          sha256: [0-9a-f]{64}$/m,
              `          sha256: ${asset.sha256}`,
              `${name} ${architecture} SHA-256`
            );
            return result;
          },
          `${name} ${architecture}`
        );
      }
      return updated;
    }, name);

  let tools = rewriteTool(
    toolsText,
    "  devpod:\n",
    "  hauler:\n",
    "DevPod",
    metadata.devpod
  );
  tools = rewriteTool(
    tools,
    "  hauler:\n",
    "fixtures:\n",
    "Hauler",
    metadata.hauler
  );
  tools = rewriteBlock(tools, "  room:\n", "  lifecycle:\n", (block) =>
    replaceOne(
      block,
      /^    commit: \S+$/m,
      `    commit: ${metadata.room.commit}`,
      "Room commit"
    ), "Room");

  let rcc = replaceOne(
    rccText,
    /^commit: \S+$/m,
    `commit: ${metadata.rcc.commit}`,
    "RCC commit"
  );
  rcc = replaceOne(rcc, /^url: \S+$/m, `url: ${metadata.rcc.assets["linux64"].url}`, "RCC URL");
  rcc = replaceOne(
    rcc,
    /^sha256: [0-9a-f]{64}$/m,
    `sha256: ${metadata.rcc.assets["linux64"].sha256}`,
    "RCC SHA-256"
  );
  return { tools, rcc };
}

export async function writeLockfilesAtomically(files, fs = { rename, rm, writeFile }) {
  const transaction = `${process.pid}-${randomUUID()}`;
  const states = files.map(({ path, contents }) => ({
    path,
    contents,
    temporary: join(dirname(path), `.${basename(path)}.renovate-${transaction}.next`),
    backup: join(dirname(path), `.${basename(path)}.renovate-${transaction}.backup`),
    backedUp: false,
    installed: false
  }));

  try {
    for (const state of states) {
      await fs.writeFile(state.temporary, state.contents, {
        encoding: "utf8",
        flag: "wx",
        mode: 0o600
      });
    }
    for (const state of states) {
      await fs.rename(state.path, state.backup);
      state.backedUp = true;
    }
    for (const state of states) {
      await fs.rename(state.temporary, state.path);
      state.installed = true;
    }
  } catch (error) {
    const rollbackErrors = [];
    for (const state of [...states].reverse()) {
      try {
        if (state.installed) {
          await fs.rm(state.path, { force: true });
          state.installed = false;
        }
        if (state.backedUp) {
          await fs.rename(state.backup, state.path);
          state.backedUp = false;
        }
      } catch (rollbackError) {
        rollbackErrors.push(rollbackError);
      }
    }
    if (rollbackErrors.length > 0) {
      throw new AggregateError([error, ...rollbackErrors], "lock update and rollback failed");
    }
    throw error;
  } finally {
    for (const state of states) {
      await fs.rm(state.temporary, { force: true });
    }
  }

  for (const state of states) {
    await fs.rm(state.backup, { force: true });
    state.backedUp = false;
  }
}

export async function refresh(fetchImpl = fetch) {
  const toolsPath = join(root, "tools.lock.yaml");
  const rccPath = join(root, "developer", "rcc.lock.yaml");
  const [toolsText, rccText] = await Promise.all([
    readFile(toolsPath, "utf8"),
    readFile(rccPath, "utf8")
  ]);

  const versions = {
    devpod: field(toolsText.slice(toolsText.indexOf("  devpod:\n"), toolsText.indexOf("  hauler:\n")), "version"),
    hauler: field(toolsText.slice(toolsText.indexOf("  hauler:\n"), toolsText.indexOf("fixtures:\n")), "version"),
    room: field(toolsText.slice(toolsText.indexOf("  room:\n"), toolsText.indexOf("  lifecycle:\n")), "version"),
    rcc: field(rccText, "version")
  };
  const haulerVersion = versions.hauler.replace(/^v/, "");
  const [devpod, hauler, room, rcc] = await Promise.all([
    releaseMetadata("skevetter/devpod", versions.devpod, ["devpod-linux-amd64", "devpod-linux-arm64"], fetchImpl),
    releaseMetadata("hauler-dev/hauler", versions.hauler, [
      `hauler_${haulerVersion}_linux_amd64.tar.gz`,
      `hauler_${haulerVersion}_linux_arm64.tar.gz`
    ], fetchImpl),
    releaseMetadata("joshyorko/room-of-requirement", versions.room, [], fetchImpl),
    releaseMetadata("joshyorko/rcc", versions.rcc, ["rcc-linux64"], fetchImpl)
  ]);
  const updated = applyMetadata(toolsText, rccText, {
    devpod: {
      commit: devpod.commit,
      assets: {
        amd64: devpod.assets["devpod-linux-amd64"],
        arm64: devpod.assets["devpod-linux-arm64"]
      }
    },
    hauler: {
      commit: hauler.commit,
      assets: {
        amd64: hauler.assets[`hauler_${haulerVersion}_linux_amd64.tar.gz`],
        arm64: hauler.assets[`hauler_${haulerVersion}_linux_arm64.tar.gz`]
      }
    },
    room,
    rcc: { commit: rcc.commit, assets: { linux64: rcc.assets["rcc-linux64"] } }
  });

  await writeLockfilesAtomically([
    { path: toolsPath, contents: updated.tools },
    { path: rccPath, contents: updated.rcc }
  ]);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  refresh().catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}

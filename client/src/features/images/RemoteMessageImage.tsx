import * as Crypto from "expo-crypto";
import { Directory, File, Paths } from "expo-file-system";
import { requireNativeModule } from "expo-modules-core";
import { memo, useContext, useEffect, useState } from "react";
import { Platform } from "react-native";

import { listConnections } from "@/db/connections";
import { downloadSSHFile } from "@/native/sshTransport";
import { CachedMessageImage } from "./CachedMessageImage";
import { ImageLoadGateContext } from "./ImageLoadGate";

const downloads = new Map<string, Promise<string>>();

export const RemoteMessageImage = memo(function RemoteMessageImage({ profileId, remotePath,
  filename, testID }: {
  profileId: string;
  remotePath: string;
  filename: string;
  testID?: string;
}) {
  const loadGate = useContext(ImageLoadGateContext);
  const [uri, setUri] = useState<string | null>(null);
  useEffect(() => {
    let active = true;
    let started = false;
    const resolve = () => {
      if (!active || started) return;
      started = true;
      void resolveRemoteImage(profileId, remotePath, filename)
        .then((value) => { if (active) setUri(value); })
        .catch(() => { if (active) setUri(""); });
    };
    const cancelWait = loadGate?.runWhenReady(resolve) ?? (() => undefined);
    if (!loadGate) resolve();
    return () => {
      active = false;
      cancelWait();
    };
  }, [filename, loadGate, profileId, remotePath]);
  return <CachedMessageImage uri={uri ?? ""} filename={filename}
    {...(testID ? { testID } : {})} />;
});

async function resolveRemoteImage(profileId: string, remotePath: string,
  filename: string): Promise<string> {
  if (!remotePath.startsWith("/")) throw new Error("远端图片路径必须是绝对路径");
  const key = `${profileId}\0${remotePath}`;
  const existing = downloads.get(key);
  if (existing) return existing;
  const pending = downloadAndCache(profileId, remotePath, filename);
  downloads.set(key, pending);
  try {
    return await pending;
  } catch (error) {
    downloads.delete(key);
    throw error;
  }
}

async function downloadAndCache(profileId: string, remotePath: string,
  filename: string): Promise<string> {
  const digest = await Crypto.digestStringAsync(Crypto.CryptoDigestAlgorithm.SHA256,
    `${profileId}\0${remotePath}`);
  const extension = imageExtension(filename) ?? imageExtension(remotePath) ?? ".img";
  const directory = new Directory(Paths.cache, "message-images");
  directory.create({ idempotent: true, intermediates: true });
  const target = new File(directory, `${digest}${extension}`);
  if (target.exists && (target.size ?? 0) > 0) return imageURI(target.uri);
  const connection = (await listConnections()).find((item) => item.profileId === profileId);
  if (!connection) throw new Error("图片所属连接不存在");
  await downloadSSHFile(connection, remotePath, target.uri);
  if (!target.exists || (target.size ?? 0) <= 0) throw new Error("图片缓存不完整");
  return imageURI(target.uri);
}

async function imageURI(fileURI: string): Promise<string> {
  if (Platform.OS !== "android") return fileURI;
  return requireNativeModule<{ getContentUriAsync(uri: string): Promise<string> }>(
    "ExponentFileSystem").getContentUriAsync(fileURI);
}

function imageExtension(value: string): string | null {
  return value.match(/\.(?:avif|gif|heic|heif|jpe?g|png|webp)$/i)?.[0]?.toLowerCase() ?? null;
}

import { requireNativeModule } from "expo-modules-core";

import { getSSHCredentials, type SSHConnection } from "@/db/connections";
import { normalizeNativeTransportError } from "./nativeError";

export type SSHAppServerEndpoint = {
  url: string;
  token: string;
};

type NativeSSHTransport = {
  openAppServer(options: {
    profileId: string;
    host: string;
    port: number;
    user: string;
    privateKey: string;
    passphrase: string | null;
    expectedHostFingerprint: string | null;
  }): Promise<SSHAppServerEndpoint>;
  close(profileId: string): Promise<void>;
  generateEd25519Key(): Promise<{ privateKey: string; publicKey: string; fingerprint: string }>;
  inspectPrivateKey(privateKey: string, passphrase: string | null): Promise<{
    publicKey: string;
    fingerprint: string;
  }>;
  probeHost(options: Record<string, unknown>): Promise<{ fingerprint: string }>;
  listDirectory(options: Record<string, unknown>): Promise<{
    name: string;
    path: string;
    directory: boolean;
  }[]>;
  uploadAttachment(options: Record<string, unknown>): Promise<{
    remotePath: string;
    sha256: string;
  }>;
};

function nativeModule(): NativeSSHTransport {
  return requireNativeModule<NativeSSHTransport>("TyrsSSHTransport");
}

async function nativeCall<T>(call: () => Promise<T>): Promise<T> {
  try {
    return await call();
  } catch (error) {
    throw normalizeNativeTransportError(error);
  }
}

export async function openSSHAppServer(connection: SSHConnection): Promise<SSHAppServerEndpoint> {
  const credentials = await getSSHCredentials(connection);
  return nativeCall(() => nativeModule().openAppServer({
    profileId: connection.profileId,
    host: connection.host,
    port: connection.port,
    user: connection.user,
    privateKey: credentials.privateKey,
    passphrase: credentials.passphrase,
    expectedHostFingerprint: connection.hostFingerprint,
  }));
}

async function connectionOptions(connection: SSHConnection): Promise<Record<string, unknown>> {
  const credentials = await getSSHCredentials(connection);
  return { profileId: connection.profileId, host: connection.host, port: connection.port,
    user: connection.user, privateKey: credentials.privateKey,
    passphrase: credentials.passphrase, expectedHostFingerprint: connection.hostFingerprint };
}

export async function probeSSHHost(connection: SSHConnection): Promise<string> {
  const options = await connectionOptions(connection);
  return (await nativeCall(() => nativeModule().probeHost(options))).fingerprint;
}

export async function probeSSHHostAddress(host: string, port: number,
  user: string): Promise<string> {
  return (await nativeCall(() => nativeModule().probeHost({ host, port, user }))).fingerprint;
}

export async function listSSHDirectory(connection: SSHConnection, path: string) {
  const options = await connectionOptions(connection);
  return nativeCall(() => nativeModule().listDirectory({ ...options, path }));
}

export async function listSSHDirectoryAddress(options: {
  profileId: string;
  host: string;
  port: number;
  user: string;
  privateKey: string;
  passphrase: string | null;
  expectedHostFingerprint: string;
}, path: string) {
  return nativeCall(() => nativeModule().listDirectory({ ...options, path }));
}

export async function uploadSSHAttachment(connection: SSHConnection, attachment: {
  uri: string;
  name: string;
  mimeType: string | null;
}) {
  const options = await connectionOptions(connection);
  return nativeCall(() => nativeModule().uploadAttachment({ ...options,
    localPath: attachment.uri.replace(/^file:\/\//, ""), filename: attachment.name,
    mimeType: attachment.mimeType }));
}

export const sshTransport = {
  close: (profileId: string) => nativeCall(() => nativeModule().close(profileId)),
  generateEd25519Key: () => nativeCall(() => nativeModule().generateEd25519Key()),
  inspectPrivateKey: (privateKey: string, passphrase: string | null) =>
    nativeCall(() => nativeModule().inspectPrivateKey(privateKey, passphrase)),
};

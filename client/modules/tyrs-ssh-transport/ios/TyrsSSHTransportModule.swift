import ExpoModulesCore
import Sshtransport

public final class TyrsSSHTransportModule: Module {
  public func definition() -> ModuleDefinition {
    Name("TyrsSSHTransport")

    AsyncFunction("openAppServer") { (options: [String: Any]) -> [String: Any] in
      try parseObject(SshtransportOpenAppServer(
        string(options, "profileId"), string(options, "host"), int64(options, "port"),
        string(options, "user"), string(options, "privateKey"),
        optionalString(options, "passphrase"),
        optionalString(options, "expectedHostFingerprint")
      ))
    }

    AsyncFunction("close") { (profileId: String) in
      SshtransportClose(profileId)
    }

    AsyncFunction("generateEd25519Key") { () -> [String: Any] in
      try parseObject(SshtransportGenerateEd25519Key())
    }

    AsyncFunction("inspectPrivateKey") { (privateKey: String, passphrase: String?) -> [String: Any] in
      try parseObject(SshtransportInspectPrivateKey(privateKey, passphrase ?? ""))
    }

    AsyncFunction("probeHost") { (options: [String: Any]) -> [String: Any] in
      ["fingerprint": try SshtransportProbeHost(
        string(options, "host"), int64(options, "port"), string(options, "user")
      )]
    }

    AsyncFunction("listDirectory") { (options: [String: Any]) -> [Any] in
      try parseArray(SshtransportListDirectory(
        string(options, "host"), int64(options, "port"), string(options, "user"),
        string(options, "privateKey"), optionalString(options, "passphrase"),
        optionalString(options, "expectedHostFingerprint"), string(options, "path")
      ))
    }

    AsyncFunction("uploadAttachment") { (options: [String: Any]) -> [String: Any] in
      try parseObject(SshtransportUploadAttachment(
        string(options, "host"), int64(options, "port"), string(options, "user"),
        string(options, "privateKey"), optionalString(options, "passphrase"),
        optionalString(options, "expectedHostFingerprint"), string(options, "localPath"),
        string(options, "filename"), optionalString(options, "mimeType")
      ))
    }
  }
}

private enum SSHTransportError: Error {
  case missingOption(String)
  case invalidJSON
}

private func string(_ options: [String: Any], _ name: String) throws -> String {
  guard let value = options[name] as? String else {
    throw SSHTransportError.missingOption(name)
  }
  return value
}

private func optionalString(_ options: [String: Any], _ name: String) -> String {
  options[name] as? String ?? ""
}

private func int64(_ options: [String: Any], _ name: String) throws -> Int64 {
  guard let value = options[name] as? NSNumber else {
    throw SSHTransportError.missingOption(name)
  }
  return value.int64Value
}

private func parseObject(_ encoded: String) throws -> [String: Any] {
  guard let data = encoded.data(using: .utf8),
        let value = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
    throw SSHTransportError.invalidJSON
  }
  return value
}

private func parseArray(_ encoded: String) throws -> [Any] {
  guard let data = encoded.data(using: .utf8),
        let value = try JSONSerialization.jsonObject(with: data) as? [Any] else {
    throw SSHTransportError.invalidJSON
  }
  return value
}

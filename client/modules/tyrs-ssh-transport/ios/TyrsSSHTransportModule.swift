import ExpoModulesCore
import Sshtransport

public final class TyrsSSHTransportModule: Module {
  public func definition() -> ModuleDefinition {
    Name("TyrsSSHTransport")

    AsyncFunction("openAppServer") { (options: [String: Any]) -> [String: Any] in
      try parseObject(goString { error in try SshtransportOpenAppServer(
        string(options, "profileId"), string(options, "host"), integer(options, "port"),
        string(options, "user"), string(options, "privateKey"),
        optionalString(options, "passphrase"),
        optionalString(options, "expectedHostFingerprint"), error
      ) })
    }

    AsyncFunction("close") { (profileId: String) in
      SshtransportClose(profileId)
    }

    AsyncFunction("inspectRuntime") { (options: [String: Any]) -> [String: Any] in
      try parseObject(goString { error in try SshtransportInspectRuntime(
        string(options, "host"), integer(options, "port"), string(options, "user"),
        string(options, "privateKey"), optionalString(options, "passphrase"),
        string(options, "expectedHostFingerprint"), error
      ) })
    }

    AsyncFunction("generateEd25519Key") { () -> [String: Any] in
      try parseObject(goString { SshtransportGenerateEd25519Key($0) })
    }

    AsyncFunction("inspectPrivateKey") { (privateKey: String, passphrase: String?) -> [String: Any] in
      try parseObject(goString { SshtransportInspectPrivateKey(privateKey, passphrase ?? "", $0) })
    }

    AsyncFunction("probeHost") { (options: [String: Any]) -> [String: Any] in
      ["fingerprint": try goString { error in try SshtransportProbeHost(
        string(options, "host"), integer(options, "port"), string(options, "user"), error
      ) }]
    }

    AsyncFunction("listDirectory") { (options: [String: Any]) -> [Any] in
      try parseArray(goString { error in try SshtransportListDirectory(
        string(options, "host"), integer(options, "port"), string(options, "user"),
        string(options, "privateKey"), optionalString(options, "passphrase"),
        optionalString(options, "expectedHostFingerprint"), string(options, "path"), error
      ) })
    }

    AsyncFunction("uploadAttachment") { (options: [String: Any]) -> [String: Any] in
      try parseObject(goString { error in try SshtransportUploadAttachment(
        string(options, "host"), integer(options, "port"), string(options, "user"),
        string(options, "privateKey"), optionalString(options, "passphrase"),
        optionalString(options, "expectedHostFingerprint"), string(options, "localPath"),
        string(options, "filename"), optionalString(options, "mimeType"), error
      ) })
    }

    AsyncFunction("downloadFile") { (options: [String: Any]) -> [String: Any] in
      try parseObject(goString { error in try SshtransportDownloadFile(
        string(options, "host"), integer(options, "port"), string(options, "user"),
        string(options, "privateKey"), optionalString(options, "passphrase"),
        optionalString(options, "expectedHostFingerprint"), string(options, "remotePath"),
        string(options, "localPath"), error
      ) })
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

private func integer(_ options: [String: Any], _ name: String) throws -> Int {
  guard let value = options[name] as? NSNumber else {
    throw SSHTransportError.missingOption(name)
  }
  return value.intValue
}

// gomobile 返回非空 NSString，Swift 不会自动把 NSError 指针转换为 throws。
private func goString(_ call: (NSErrorPointer) throws -> String) throws -> String {
  var error: NSError?
  let value = try call(&error)
  if let error { throw error }
  return value
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

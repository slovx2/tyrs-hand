package com.tyrshand.sshtransport

import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition
import org.json.JSONArray
import org.json.JSONObject
import sshtransport.Sshtransport

class TyrsSSHTransportModule : Module() {
  override fun definition() = ModuleDefinition {
    Name("TyrsSSHTransport")

    AsyncFunction("openAppServer") { options: Map<String, Any?> ->
      parseObject(Sshtransport.openAppServer(
        options.string("profileId"), options.string("host"), options.long("port"),
        options.string("user"), options.string("privateKey"), options.optionalString("passphrase"),
        options.optionalString("expectedHostFingerprint"),
      ))
    }

    AsyncFunction("close") { profileId: String ->
      Sshtransport.close(profileId)
    }

    AsyncFunction("generateEd25519Key") {
      parseObject(Sshtransport.generateEd25519Key())
    }

    AsyncFunction("inspectPrivateKey") { privateKey: String, passphrase: String? ->
      parseObject(Sshtransport.inspectPrivateKey(privateKey, passphrase ?: ""))
    }

    AsyncFunction("probeHost") { options: Map<String, Any?> ->
      mapOf("fingerprint" to Sshtransport.probeHost(
        options.string("host"), options.long("port"), options.string("user"),
      ))
    }

    AsyncFunction("listDirectory") { options: Map<String, Any?> ->
      parseArray(Sshtransport.listDirectory(
        options.string("host"), options.long("port"), options.string("user"),
        options.string("privateKey"), options.optionalString("passphrase"),
        options.optionalString("expectedHostFingerprint"), options.string("path"),
      ))
    }

    AsyncFunction("uploadAttachment") { options: Map<String, Any?> ->
      parseObject(Sshtransport.uploadAttachment(
        options.string("host"), options.long("port"), options.string("user"),
        options.string("privateKey"), options.optionalString("passphrase"),
        options.optionalString("expectedHostFingerprint"), options.string("localPath"),
        options.string("filename"), options.optionalString("mimeType"),
      ))
    }
  }
}

private fun Map<String, Any?>.string(name: String): String =
  this[name] as? String ?: throw IllegalArgumentException("缺少 $name")

private fun Map<String, Any?>.optionalString(name: String): String = this[name] as? String ?: ""

private fun Map<String, Any?>.long(name: String): Long =
  (this[name] as? Number)?.toLong() ?: throw IllegalArgumentException("缺少 $name")

private fun parseObject(encoded: String): Map<String, Any?> = jsonObject(JSONObject(encoded))

private fun parseArray(encoded: String): List<Any?> = jsonArray(JSONArray(encoded))

private fun jsonObject(value: JSONObject): Map<String, Any?> = value.keys().asSequence()
  .associateWith { key -> jsonValue(value.get(key)) }

private fun jsonArray(value: JSONArray): List<Any?> =
  (0 until value.length()).map { index -> jsonValue(value.get(index)) }

private fun jsonValue(value: Any?): Any? = when (value) {
  JSONObject.NULL -> null
  is JSONObject -> jsonObject(value)
  is JSONArray -> jsonArray(value)
  else -> value
}

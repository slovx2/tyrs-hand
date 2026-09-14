package com.tyrshand.audioroute

import android.content.Context
import android.media.AudioAttributes
import android.media.AudioDeviceInfo
import android.media.AudioDeviceCallback
import android.media.AudioFocusRequest
import android.media.AudioManager
import android.media.MediaPlayer
import android.media.MediaRecorder
import android.os.Build
import android.os.Handler
import android.os.Looper
import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

class TyrsAudioRouteModule : Module() {
  private var prepared = false
  private var previousMode = AudioManager.MODE_NORMAL
  private var previousSpeakerphoneOn = false
  private var previousBluetoothScoOn = false
  private var previousCommunicationDevice: AudioDeviceInfo? = null
  private var automaticSelection = true
  private var manualDeviceId: Int? = null
  private var lastDeviceFingerprint: String? = null
  private var monitoredAudioManager: AudioManager? = null
  private var audioFocusManager: AudioManager? = null
  private var audioFocusRequest: AudioFocusRequest? = null
  private val audioFocusChangeListener = AudioManager.OnAudioFocusChangeListener { }
  private val audioDeviceCallback = object : AudioDeviceCallback() {
    override fun onAudioDevicesAdded(addedDevices: Array<out AudioDeviceInfo>) {
      notifyRouteChanged()
    }

    override fun onAudioDevicesRemoved(removedDevices: Array<out AudioDeviceInfo>) {
      notifyRouteChanged()
    }
  }

  override fun definition() = ModuleDefinition {
    Name("TyrsAudioRoute")
    Events("onLiveAudioRouteChanged")

    OnStartObserving("onLiveAudioRouteChanged") {
      startMonitoring(requireContext())
    }
    OnStopObserving("onLiveAudioRouteChanged") {
      stopMonitoring()
    }
    OnDestroy {
      stopMonitoring()
    }

    AsyncFunction("prepareLiveAudioRoute") {
      prepare(requireContext())
    }

    AsyncFunction("setLiveAudioRoute") { kind: String, deviceId: Int? ->
      setRoute(requireContext(), kind, deviceId)
    }

    AsyncFunction("restoreLiveAudioRoute") {
      restore(requireContext())
    }

    AsyncFunction("getLiveAudioRoute") {
      snapshot(requireContext())
    }

    AsyncFunction("playLiveCue") { kind: String ->
      playCue(requireContext(), kind)
    }
  }

  private fun requireContext(): Context =
    appContext.reactContext ?: throw IllegalStateException("应用上下文不可用")

  private fun startMonitoring(context: Context) {
    val audioManager = context.getSystemService(AudioManager::class.java) ?: return
    if (monitoredAudioManager === audioManager) return
    monitoredAudioManager = audioManager
    audioManager.registerAudioDeviceCallback(audioDeviceCallback, Handler(Looper.getMainLooper()))
  }

  private fun stopMonitoring() {
    monitoredAudioManager?.unregisterAudioDeviceCallback(audioDeviceCallback)
    monitoredAudioManager = null
  }

  private fun notifyRouteChanged() {
    val route = synchronized(this) {
      try {
        val context = requireContext()
        val audioManager = context.getSystemService(AudioManager::class.java)
        if (prepared && audioManager != null) {
          val fingerprint = deviceFingerprint(availableCommunicationDevices(audioManager))
          if (fingerprint != lastDeviceFingerprint) {
            lastDeviceFingerprint = fingerprint
            audioManager.mode = AudioManager.MODE_IN_COMMUNICATION
            automaticSelection = true
            selectAutomatic(audioManager)
          }
        }
        snapshot(context)
      } catch (_: RuntimeException) {
        null
      }
    }
    if (route != null) sendEvent("onLiveAudioRouteChanged", route)
  }

  @Synchronized
  private fun prepare(context: Context): Map<String, Any?> {
    val audioManager = context.getSystemService(AudioManager::class.java)
      ?: throw IllegalStateException("AudioManager 不可用")
    val shouldSelectAutomatic = !prepared
    if (shouldSelectAutomatic) {
      automaticSelection = true
      manualDeviceId = null
      previousMode = audioManager.mode
      previousSpeakerphoneOn = audioManager.isSpeakerphoneOn
      previousBluetoothScoOn = audioManager.isBluetoothScoOn
      if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
        previousCommunicationDevice = try {
          audioManager.communicationDevice
        } catch (_: SecurityException) {
          null
        }
      }
      prepared = true
    }

    requestAudioFocus(audioManager)
    audioManager.mode = AudioManager.MODE_IN_COMMUNICATION
    if (shouldSelectAutomatic) selectAutomatic(audioManager)
    lastDeviceFingerprint = deviceFingerprint(availableCommunicationDevices(audioManager))
    return snapshot(context)
  }

  @Synchronized
  private fun setRoute(context: Context, kind: String, deviceId: Int?): Map<String, Any?> {
    val audioManager = context.getSystemService(AudioManager::class.java)
      ?: throw IllegalStateException("AudioManager 不可用")
    if (!prepared) {
      prepare(context)
    }
    audioManager.mode = AudioManager.MODE_IN_COMMUNICATION
    if (kind == "auto") {
      selectAutomatic(audioManager)
    } else {
      val devices = availableCommunicationDevices(audioManager)
      val selected = devices.firstOrNull { deviceId != null && it.id == deviceId }
        ?: devices.firstOrNull { routeKind(it) == kind }
        ?: throw IllegalArgumentException("当前没有可用的${routeName(kind)}")
      automaticSelection = false
      manualDeviceId = selected.id
      selectDevice(audioManager, selected)
    }
    lastDeviceFingerprint = deviceFingerprint(availableCommunicationDevices(audioManager))
    return snapshot(context)
  }

  private fun selectAutomatic(audioManager: AudioManager) {
    automaticSelection = true
    manualDeviceId = null
    val devices = availableCommunicationDevices(audioManager)
    val preferred = devices.firstOrNull(::isWiredCommunicationDevice)
      ?: devices.firstOrNull(::isBluetoothCommunicationDevice)
      ?: devices.firstOrNull { it.type == AudioDeviceInfo.TYPE_BUILTIN_SPEAKER }
    if (preferred != null) {
      selectDevice(audioManager, preferred)
      return
    }
    @Suppress("DEPRECATION")
    audioManager.isSpeakerphoneOn = true
    if (Build.VERSION.SDK_INT < Build.VERSION_CODES.S) {
      @Suppress("DEPRECATION")
      audioManager.stopBluetoothSco()
      @Suppress("DEPRECATION")
      audioManager.isBluetoothScoOn = false
    }
  }

  private fun selectDevice(audioManager: AudioManager, device: AudioDeviceInfo) {
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
      if (!audioManager.setCommunicationDevice(device)) {
        throw IllegalStateException("系统拒绝了音频设备切换")
      }
      return
    }
    @Suppress("DEPRECATION")
    if (isBluetoothCommunicationDevice(device)) {
      startBluetoothSco(audioManager)
    } else {
      @Suppress("DEPRECATION")
      audioManager.stopBluetoothSco()
      @Suppress("DEPRECATION")
      audioManager.isBluetoothScoOn = false
      @Suppress("DEPRECATION")
      audioManager.isSpeakerphoneOn = device.type == AudioDeviceInfo.TYPE_BUILTIN_SPEAKER
    }
  }

  @Synchronized
  private fun restore(context: Context) {
    val audioManager = context.getSystemService(AudioManager::class.java)
      ?: return
    if (!prepared) return
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
      try {
        val previousDevice = previousCommunicationDevice
        if (previousDevice != null) {
          audioManager.setCommunicationDevice(previousDevice)
        } else {
          audioManager.clearCommunicationDevice()
        }
      } catch (_: SecurityException) {
        try {
          audioManager.clearCommunicationDevice()
        } catch (_: RuntimeException) {
          // 蓝牙权限被撤销时只能继续恢复普通音频模式。
        }
      } catch (_: RuntimeException) {
        try {
          audioManager.clearCommunicationDevice()
        } catch (_: RuntimeException) {
          // 设备已断开时清除旧路由可能失败，下面仍继续恢复系统模式。
        }
      }
    } else {
      @Suppress("DEPRECATION")
      if (previousBluetoothScoOn) {
        audioManager.startBluetoothSco()
        audioManager.isBluetoothScoOn = true
      } else {
        audioManager.stopBluetoothSco()
        audioManager.isBluetoothScoOn = false
      }
    }
    @Suppress("DEPRECATION")
    audioManager.isSpeakerphoneOn = previousSpeakerphoneOn
    audioManager.mode = previousMode
    abandonAudioFocus(audioManager)
    prepared = false
    automaticSelection = true
    manualDeviceId = null
    lastDeviceFingerprint = null
    previousCommunicationDevice = null
  }

  @Synchronized
  private fun requestAudioFocus(audioManager: AudioManager) {
    if (audioFocusManager === audioManager &&
      (Build.VERSION.SDK_INT < Build.VERSION_CODES.O || audioFocusRequest != null)) return
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
      val attributes = AudioAttributes.Builder()
        .setUsage(AudioAttributes.USAGE_VOICE_COMMUNICATION)
        .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
        .build()
      val request = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT)
        .setAudioAttributes(attributes)
        .setAcceptsDelayedFocusGain(false)
        .setOnAudioFocusChangeListener(audioFocusChangeListener)
        .build()
      if (audioManager.requestAudioFocus(request) == AudioManager.AUDIOFOCUS_REQUEST_GRANTED) {
        audioFocusManager = audioManager
        audioFocusRequest = request
      }
      return
    }
    @Suppress("DEPRECATION")
    if (audioManager.requestAudioFocus(audioFocusChangeListener,
        AudioManager.STREAM_VOICE_CALL, AudioManager.AUDIOFOCUS_GAIN_TRANSIENT) ==
      AudioManager.AUDIOFOCUS_REQUEST_GRANTED) {
      audioFocusManager = audioManager
    }
  }

  @Synchronized
  private fun abandonAudioFocus(audioManager: AudioManager) {
    if (audioFocusManager !== audioManager) return
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
      audioFocusRequest?.let { audioManager.abandonAudioFocusRequest(it) }
    } else {
      @Suppress("DEPRECATION")
      audioManager.abandonAudioFocus(audioFocusChangeListener)
    }
    audioFocusRequest = null
    audioFocusManager = null
  }

  private fun availableCommunicationDevices(audioManager: AudioManager): List<AudioDeviceInfo> {
    val devices = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
      try {
        audioManager.availableCommunicationDevices.toList()
      } catch (_: SecurityException) {
        wiredDevices(audioManager)
      }
    } else {
      wiredAndBluetoothDevices(audioManager) + speakerDevices(audioManager)
    }
    return devices.filter { it.type != AudioDeviceInfo.TYPE_BUILTIN_EARPIECE }
  }

  private fun deviceFingerprint(devices: List<AudioDeviceInfo>): String =
    devices.map { it.id }.sorted().joinToString(",")

  private fun speakerDevices(audioManager: AudioManager): List<AudioDeviceInfo> =
    try {
      audioManager.getDevices(AudioManager.GET_DEVICES_OUTPUTS)
        .filter { it.type == AudioDeviceInfo.TYPE_BUILTIN_SPEAKER }
    } catch (_: SecurityException) {
      emptyList()
    }

  private fun wiredDevices(audioManager: AudioManager): List<AudioDeviceInfo> =
    try {
      audioManager.getDevices(AudioManager.GET_DEVICES_OUTPUTS)
        .filter(::isWiredCommunicationDevice)
    } catch (_: SecurityException) {
      emptyList()
    }

  private fun wiredAndBluetoothDevices(audioManager: AudioManager): List<AudioDeviceInfo> =
    try {
      audioManager.getDevices(AudioManager.GET_DEVICES_OUTPUTS)
        .filter { isWiredCommunicationDevice(it) || isBluetoothCommunicationDevice(it) }
    } catch (_: SecurityException) {
      emptyList()
    }

  @Suppress("DEPRECATION")
  private fun startBluetoothSco(audioManager: AudioManager) {
    if (!audioManager.isBluetoothScoAvailableOffCall) return
    audioManager.startBluetoothSco()
    audioManager.isBluetoothScoOn = true
    val deadline = System.currentTimeMillis() + 800
    while (!audioManager.isBluetoothScoOn && System.currentTimeMillis() < deadline) {
      Thread.sleep(40)
    }
  }

  private fun snapshot(context: Context): Map<String, Any?> {
    val audioManager = context.getSystemService(AudioManager::class.java)
      ?: throw IllegalStateException("AudioManager 不可用")
    val available = availableCommunicationDevices(audioManager)
    val active = activeDevice(audioManager)
    val activeInput = activeInputDevice(audioManager)
    val kind = routeKind(active, audioManager)
    return mapOf(
      "kind" to kind,
      "automatic" to automaticSelection,
      "activeDevice" to active?.let(::deviceMap),
      "activeInputDevice" to activeInput?.let(::deviceMap),
      "availableDevices" to available.map(::deviceMap),
      "audioMode" to audioManager.mode,
      "speakerphoneOn" to audioManager.isSpeakerphoneOn,
      "bluetoothScoOn" to audioManager.isBluetoothScoOn,
      "bluetoothCommunicationAvailable" to available.any(::isBluetoothCommunicationDevice),
    )
  }

  private fun activeInputDevice(audioManager: AudioManager): AudioDeviceInfo? {
    if (Build.VERSION.SDK_INT < Build.VERSION_CODES.N) return null
    val configurations = try {
      audioManager.activeRecordingConfigurations
    } catch (_: SecurityException) {
      return null
    }
    return configurations.firstOrNull {
      it.clientAudioSource == MediaRecorder.AudioSource.VOICE_COMMUNICATION
    }?.audioDevice
  }

  private fun activeDevice(audioManager: AudioManager): AudioDeviceInfo? {
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
      return try {
        audioManager.communicationDevice
      } catch (_: SecurityException) {
        null
      }
    }
    val outputs = try {
      audioManager.getDevices(AudioManager.GET_DEVICES_OUTPUTS).toList()
    } catch (_: SecurityException) {
      emptyList()
    }
    if (audioManager.isBluetoothScoOn) {
      outputs.firstOrNull(::isBluetoothCommunicationDevice)?.let { return it }
    }
    if (!audioManager.isSpeakerphoneOn) {
      outputs.firstOrNull(::isWiredCommunicationDevice)?.let { return it }
      outputs.firstOrNull { it.type == AudioDeviceInfo.TYPE_BUILTIN_EARPIECE }?.let { return it }
    }
    return outputs.firstOrNull { it.type == AudioDeviceInfo.TYPE_BUILTIN_SPEAKER }
  }

  private fun routeKind(active: AudioDeviceInfo?, audioManager: AudioManager): String =
    when {
      active != null -> routeKind(active)
      audioManager.isSpeakerphoneOn -> "speaker"
      else -> "unknown"
    }

  private fun deviceMap(device: AudioDeviceInfo): Map<String, Any?> = mapOf(
    "id" to device.id,
    "name" to device.productName.toString(),
    "type" to device.type,
    "kind" to routeKind(device),
  )

  private fun routeKind(device: AudioDeviceInfo): String =
    when {
      isBluetoothCommunicationDevice(device) -> "bluetooth"
      isWiredCommunicationDevice(device) -> "wired"
      device.type == AudioDeviceInfo.TYPE_BUILTIN_EARPIECE -> "earpiece"
      device.type == AudioDeviceInfo.TYPE_BUILTIN_SPEAKER -> "speaker"
      else -> "unknown"
    }

  private fun routeName(kind: String): String =
    when (kind) {
      "bluetooth" -> "蓝牙耳机"
      "wired" -> "有线耳机"
      "speaker" -> "扬声器"
      "earpiece" -> "听筒"
      else -> "音频设备"
    }

  private fun isBluetoothCommunicationDevice(device: AudioDeviceInfo): Boolean =
    device.type == AudioDeviceInfo.TYPE_BLUETOOTH_SCO ||
      (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S &&
        device.type == AudioDeviceInfo.TYPE_BLE_HEADSET)

  private fun isWiredCommunicationDevice(device: AudioDeviceInfo): Boolean =
    device.type == AudioDeviceInfo.TYPE_WIRED_HEADSET ||
      device.type == AudioDeviceInfo.TYPE_WIRED_HEADPHONES ||
      (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S &&
        device.type == AudioDeviceInfo.TYPE_USB_HEADSET)

  private fun playCue(context: Context, kind: String) {
    val resId = when (kind) {
      "connecting" -> R.raw.live_connect_start
      "connected" -> R.raw.live_connect_ready
      else -> return
    }
    val player = MediaPlayer()
    val finished = CountDownLatch(1)
    try {
      val attributes = AudioAttributes.Builder()
        .setUsage(AudioAttributes.USAGE_VOICE_COMMUNICATION)
        .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
        .build()
      player.setAudioAttributes(attributes)
      context.resources.openRawResourceFd(resId).use { fd ->
        player.setDataSource(fd.fileDescriptor, fd.startOffset, fd.length)
      }
      if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
        val audioManager = context.getSystemService(AudioManager::class.java)
        val device = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
          try { audioManager?.communicationDevice } catch (_: SecurityException) { null }
        } else null
        if (device != null) player.setPreferredDevice(device)
      }
      player.setOnCompletionListener {
        it.release()
        finished.countDown()
      }
      player.setOnErrorListener { current, _, _ ->
        current.release()
        finished.countDown()
        true
      }
      player.prepare()
      player.start()
      if (!finished.await(1800, TimeUnit.MILLISECONDS)) {
        player.release()
      }
    } catch (_: RuntimeException) {
      player.release()
    }
  }
}

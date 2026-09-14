package com.tyrshand.voice

object LiveWakeBridge {
  @Volatile
  var pendingWake: Boolean = false
  var onWake: (() -> Unit)? = null

  fun markWake() {
    pendingWake = true
    onWake?.invoke()
  }

  fun consumePendingWake(): Boolean {
    val value = pendingWake
    pendingWake = false
    return value
  }
}

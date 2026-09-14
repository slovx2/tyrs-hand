const fs = require("fs");
const path = require("path");
const { withAndroidManifest, withDangerousMod, withMainActivity } = require("expo/config-plugins");

// LiveActivity 必须生成在 app 包里。tyrs-voice 是 Expo module 库，
// 编译类路径上没有 ReactActivity，不能把该 Activity 放进模块。
function liveActivitySource(packageName) {
  return `package ${packageName}

import android.content.Intent
import android.os.Bundle
import com.facebook.react.ReactActivity
import com.facebook.react.ReactActivityDelegate
import com.facebook.react.defaults.DefaultNewArchitectureEntryPoint.fabricEnabled
import com.facebook.react.defaults.DefaultReactActivityDelegate
import com.tyrshand.voice.LiveIntents
import com.tyrshand.voice.LiveWakeBridge
import expo.modules.ReactActivityDelegateWrapper

class LiveActivity : ReactActivity() {
  override fun onCreate(savedInstanceState: Bundle?) {
    markWakeFromIntent(intent)
    super.onCreate(savedInstanceState)
  }

  override fun onNewIntent(intent: Intent) {
    super.onNewIntent(intent)
    setIntent(intent)
    markWakeFromIntent(intent)
  }

  override fun getMainComponentName(): String = "tyrsLive"

  override fun createReactActivityDelegate(): ReactActivityDelegate {
    return ReactActivityDelegateWrapper(
      this,
      BuildConfig.IS_NEW_ARCHITECTURE_ENABLED,
      object : DefaultReactActivityDelegate(
        this,
        mainComponentName,
        fabricEnabled,
      ) {},
    )
  }

  override fun invokeDefaultOnBackPressed() {
    if (isTaskRoot) {
      startActivity(Intent(this, MainActivity::class.java))
    }
    finish()
  }

  private fun markWakeFromIntent(intent: Intent?) {
    if (LiveIntents.isWake(intent)) LiveWakeBridge.markWake()
  }
}
`;
}

function withLiveActivityFile(config) {
  return withDangerousMod(config, ["android", async (config) => {
    const packageName = config.android?.package;
    if (!packageName) {
      throw new Error("withLiveIntentForward: 缺少 android.package，无法生成 LiveActivity");
    }
    const dir = path.join(
      config.modRequest.platformProjectRoot,
      "app/src/main/java",
      packageName.replace(/\./g, "/"),
    );
    fs.mkdirSync(dir, { recursive: true });
    const file = path.join(dir, "LiveActivity.kt");
    fs.writeFileSync(file, liveActivitySource(packageName));
    if (!fs.existsSync(file)) {
      throw new Error(`withLiveIntentForward: 未能写入 ${file}`);
    }
    return config;
  }]);
}

function withLiveActivityManifest(config) {
  return withAndroidManifest(config, (config) => {
    const application = config.modResults.manifest.application?.[0];
    if (!application) {
      throw new Error("withLiveIntentForward: AndroidManifest 缺少 application");
    }
    application.activity = application.activity ?? [];
    const exists = application.activity.some((item) => item.$?.["android:name"] === ".LiveActivity");
    if (!exists) {
      application.activity.push({
        $: {
          "android:name": ".LiveActivity",
          "android:exported": "false",
          "android:launchMode": "singleTop",
          "android:configChanges": "keyboard|keyboardHidden|orientation|screenSize|screenLayout|uiMode",
          "android:windowSoftInputMode": "adjustResize",
          "android:theme": "@style/AppTheme",
          "android:screenOrientation": "unspecified",
        },
      });
    }
    return config;
  });
}

function withLiveIntentForward(config) {
  config = withLiveActivityFile(config);
  config = withLiveActivityManifest(config);
  return withMainActivity(config, (config) => {
    if (config.modResults.language !== "kt") {
      throw new Error("withLiveIntentForward: MainActivity 不是 Kotlin");
    }
    let src = config.modResults.contents;
    if (!src.includes("forwardLiveIntent")) {
      if (!src.includes("import android.content.Intent")) {
        if (!src.includes("import android.os.Bundle")) {
          throw new Error("withLiveIntentForward: MainActivity 缺少 Bundle import，无法插入 Intent");
        }
        src = src.replace(
          "import android.os.Bundle",
          "import android.content.Intent\nimport android.os.Bundle",
        );
      }
      const needle = "super.onCreate(null)\n  }";
      if (!src.includes(needle)) {
        throw new Error("withLiveIntentForward: 无法在 MainActivity.onCreate 中插入 Live 转发");
      }
      src = src.replace(
        needle,
        "super.onCreate(null)\n    forwardLiveIntent(intent)\n  }\n\n"
          + "  override fun onNewIntent(intent: Intent) {\n"
          + "    super.onNewIntent(intent)\n"
          + "    setIntent(intent)\n"
          + "    forwardLiveIntent(intent)\n"
          + "  }\n\n"
          + "  private fun forwardLiveIntent(intent: Intent?) {\n"
          + "    if (!com.tyrshand.voice.LiveIntents.isLiveUri(intent)) return\n"
          + "    startActivity(com.tyrshand.voice.LiveIntents.intent(this, com.tyrshand.voice.LiveIntents.isWake(intent)))\n"
          + "  }",
      );
    }
    if (!src.includes("forwardLiveIntent")) {
      throw new Error("withLiveIntentForward: MainActivity 未包含 Live 转发");
    }
    config.modResults.contents = src;
    return config;
  });
}

module.exports = withLiveIntentForward;

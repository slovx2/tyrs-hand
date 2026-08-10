require 'json'
require 'shellwords'

package = JSON.parse(File.read(File.join(__dir__, '..', 'package.json')))
repository_root = File.expand_path('../../../..', __dir__)
framework_path = File.join(__dir__, 'Sshtransport.xcframework')

Pod::Spec.new do |s|
  s.name = 'TyrsSSHTransport'
  s.version = package['version']
  s.summary = package['description']
  s.description = package['description']
  s.license = package['license']
  s.author = package['author']
  s.homepage = package['homepage']
  s.platforms = { :ios => '15.1' }
  s.swift_version = '5.9'
  s.source = { :path => '.' }
  s.static_framework = true
  s.dependency 'ExpoModulesCore'
  s.source_files = '**/*.swift'
  s.vendored_frameworks = 'Sshtransport.xcframework'
  s.pod_target_xcconfig = { 'DEFINES_MODULE' => 'YES' }
  s.prepare_command = <<-CMD
    set -euo pipefail
    cd #{repository_root.shellescape}
    go run golang.org/x/mobile/cmd/gomobile@v0.0.0-20260803200217-62cee1672c8e init
    export PATH="$(go env GOPATH)/bin:$PATH"
    rm -rf #{framework_path.shellescape}
    go run golang.org/x/mobile/cmd/gomobile@v0.0.0-20260803200217-62cee1672c8e bind \
      -target=ios -o #{framework_path.shellescape} ./mobile/sshtransport
  CMD
end

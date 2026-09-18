# Changelog

## [0.10.0](https://github.com/wangnan0916/ssh-forward/compare/v0.9.0...v0.10.0) (2026-09-18)


### Features

* redraw status --watch in place on a terminal ([2af3675](https://github.com/wangnan0916/ssh-forward/commit/2af367505452bd4dd8a19d51f8e80c9c389b0ea2))

## [0.9.0](https://github.com/wangnan0916/ssh-forward/compare/v0.8.0...v0.9.0) (2026-09-18)


### Features

* omit SSH listeners and enrich Docker published ports ([#24](https://github.com/wangnan0916/ssh-forward/issues/24)) ([5c7619e](https://github.com/wangnan0916/ssh-forward/commit/5c7619eef26c4bafebbec51217e487b09f59eda6))


### Bug Fixes

* auto-monitor discovered SSH hosts without host add ([#22](https://github.com/wangnan0916/ssh-forward/issues/22)) ([90fbdbf](https://github.com/wangnan0916/ssh-forward/commit/90fbdbf0076c7e87b6d5f709af64a27fea868db0))

## [0.8.0](https://github.com/wangnan0916/ssh-forward/compare/v0.7.0...v0.8.0) (2026-09-18)


### ⚠ BREAKING CHANGES

* discover and monitor SSH hosts concurrently ([#19](https://github.com/wangnan0916/ssh-forward/issues/19))

### Features

* discover and monitor SSH hosts concurrently ([#19](https://github.com/wangnan0916/ssh-forward/issues/19)) ([f56d434](https://github.com/wangnan0916/ssh-forward/commit/f56d434db14ecf0ba6628a829fe54717b49e534c))

## [0.7.0](https://github.com/wangnan0916/ssh-forward/compare/v0.6.0...v0.7.0) (2026-09-08)


### ⚠ BREAKING CHANGES

* imported ports now listen on 0.0.0.0 instead of loopback only.

### Features

* expose imported ports on local networks ([ac5cea8](https://github.com/wangnan0916/ssh-forward/commit/ac5cea80724de1fdf438dc453b1140976bb29bd9))

## [0.6.0](https://github.com/wangnan0916/ssh-forward/compare/v0.5.0...v0.6.0) (2026-08-25)


### Features

* publish local services over SSH ([#13](https://github.com/wangnan0916/ssh-forward/issues/13)) ([d690511](https://github.com/wangnan0916/ssh-forward/commit/d690511faa089ea48ea758030d9c0f2d2f62094d))

## [0.5.0](https://github.com/wangnan0916/ssh-forward/compare/v0.4.0...v0.5.0) (2026-08-25)


### Features

* improve forwarding reliability and diagnostics ([#11](https://github.com/wangnan0916/ssh-forward/issues/11)) ([8a3484a](https://github.com/wangnan0916/ssh-forward/commit/8a3484afa6594f3d38997997b07725cc59592cf1))

## [0.4.0](https://github.com/wangnan0916/ssh-forward/compare/v0.3.0...v0.4.0) (2026-08-24)


### Features

* add terminal links to forward targets ([#7](https://github.com/wangnan0916/ssh-forward/issues/7)) ([235d19b](https://github.com/wangnan0916/ssh-forward/commit/235d19b5702f6de2fa339a55766ec5545b710b34))
* forward ports by working directory glob ([#9](https://github.com/wangnan0916/ssh-forward/issues/9)) ([f0fde4e](https://github.com/wangnan0916/ssh-forward/commit/f0fde4ea61adc30a6d9bd54a5225a87dfa43e9fa))

## [0.3.0](https://github.com/wangnan0916/ssh-forward/compare/v0.2.0...v0.3.0) (2026-08-24)


### Features

* render readable status tables ([8cff65c](https://github.com/wangnan0916/ssh-forward/commit/8cff65cbc8a3585b7785745fbea4639004fe15df))

## [0.2.0](https://github.com/wangnan0916/ssh-forward/compare/v0.1.0...v0.2.0) (2026-08-24)


### Features

* enrich listener discovery status ([c69d5e4](https://github.com/wangnan0916/ssh-forward/commit/c69d5e47cbac459cdbb8f231bd0aa01453862632))

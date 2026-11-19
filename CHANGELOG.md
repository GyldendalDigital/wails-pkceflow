# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- Changelog tracking for upcoming releases.
- README section recommending Wails `SingleInstance` for desktop apps whose
  provider rotates refresh tokens. Instances that share one token store race on
  every rotation, and the instance that loses can end the session for all of
  them.

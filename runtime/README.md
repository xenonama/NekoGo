# runtime/ — files bundled next to the executables

Place these files here before running `build.bat` (or let the script warn):

- WinDivert.dll / WinDivert64.sys — https://github.com/basil00/Divert/releases
  (needed for sing-box TUN mode)

Anything present here is copied to `gui/build/bin` and staged into the
portable ZIP / installer automatically.

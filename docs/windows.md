# Windows build notes

`go-sqlite3` requires **CGO to be enabled** at compile time. By default, CGO is disabled on Windows, so you need to enable it and have a C compiler on `PATH`.

1. **Install a C compiler.** [MSYS2](https://www.msys2.org/) is the easiest path — after installing, add the `ucrt64\bin` folder to your `PATH`. A step-by-step guide is available [here](https://code.visualstudio.com/docs/cpp/config-mingw).

2. **Enable CGO and build.**

   ```bash
   go env -w CGO_ENABLED=1
   make build
   ```

Without this setup, the build fails with:

> `Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo to work.`

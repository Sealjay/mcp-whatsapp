# Windows build

`go-sqlite3` needs CGO and a C compiler. Install [MSYS2](https://www.msys2.org/), then in its UCRT64 terminal install GCC:

```sh
pacman -S --needed mingw-w64-ucrt-x86_64-gcc
```

Add the MSYS2 `ucrt64\bin` directory (usually `C:\msys64\ucrt64\bin`) to `PATH`. The [MinGW setup guide](https://code.visualstudio.com/docs/cpp/config-mingw) has more detail.

In PowerShell, enable CGO and build the executable directly with Go:

```powershell
go env -w CGO_ENABLED=1
New-Item -ItemType Directory -Force bin
go build -o .\bin\whatsapp-mcp.exe .\cmd\whatsapp-mcp
```

The direct Go command avoids requiring GNU Make and Bash on Windows. If CGO is disabled or GCC is missing from `PATH`, the build reports that `go-sqlite3` requires cgo.

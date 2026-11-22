# Third-party notices

pressurecooker is distributed under the [MIT License](LICENSE). Release binaries are compiled with Go (`CGO_ENABLED=0`) and statically link the Go standard library, the Go runtime, and the module dependencies listed in `go.mod`.

## Included components

| Component | Version | Scope | License | Copyright notice |
| --- | --- | --- | --- | --- |
| `pressurecooker` | repo | project | MIT | Copyright (c) 2026 Pressurecooker contributors |
| Go standard library and runtime | `go 1.27.4` | runtime | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `github.com/aymanbagabas/go-osc52/v2` | `v2.0.1` | indirect | MIT | Copyright (c) 2022 Ayman Bagabas |
| `github.com/charmbracelet/bubbles` | `v1.0.0` | direct | MIT | Copyright (c) 2020-2025 Charmbracelet, Inc |
| `github.com/charmbracelet/bubbletea` | `v1.3.10` | direct | MIT | Copyright (c) 2020-2025 Charmbracelet, Inc |
| `github.com/charmbracelet/colorprofile` | `v0.4.1` | indirect | MIT | Copyright (c) 2020-2024 Charmbracelet, Inc |
| `github.com/charmbracelet/lipgloss` | `v1.1.0` | direct | MIT | Copyright (c) 2021-2023 Charmbracelet, Inc |
| `github.com/charmbracelet/x/ansi` | `v0.11.6` | indirect | MIT | Copyright (c) 2023 Charmbracelet, Inc. |
| `github.com/charmbracelet/x/cellbuf` | `v0.0.15` | indirect | MIT | Copyright (c) 2023 Charmbracelet, Inc. |
| `github.com/charmbracelet/x/term` | `v0.2.2` | indirect | MIT | Copyright (c) 2023 Charmbracelet, Inc. |
| `github.com/clipperhouse/displaywidth` | `v0.9.0` | indirect | MIT | Copyright (c) 2025 Matt Sherman |
| `github.com/clipperhouse/stringish` | `v0.1.1` | indirect | MIT | Copyright (c) 2025 Matt Sherman |
| `github.com/clipperhouse/uax29/v2` | `v2.5.0` | indirect | MIT | Copyright (c) 2020 Matt Sherman |
| `github.com/erikgeiser/coninput` | `v0.0.0-20211004153227-1c3628e74d0f` | indirect | MIT | Copyright (c) 2021 Erik G. |
| `github.com/lucasb-eyer/go-colorful` | `v1.3.0` | indirect | MIT | Copyright (c) 2013 Lucas Beyer |
| `github.com/mattn/go-isatty` | `v0.0.20` | indirect | MIT | Copyright (c) Yasuhiro MATSUMOTO <mattn.jp@gmail.com> |
| `github.com/mattn/go-localereader` | `v0.0.1` | indirect | MIT | Yasuhiro Matsumoto (declared MIT in README.md) |
| `github.com/mattn/go-runewidth` | `v0.0.19` | indirect | MIT | Copyright (c) 2016 Yasuhiro Matsumoto |
| `github.com/muesli/ansi` | `v0.0.0-20230316100256-276c6243b2f6` | indirect | MIT | Copyright (c) 2021 Christian Muehlhaeuser |
| `github.com/muesli/cancelreader` | `v0.2.2` | indirect | MIT | Copyright (c) 2022 Erik Geiser and Christian Muehlhaeuser |
| `github.com/muesli/termenv` | `v0.16.0` | indirect | MIT | Copyright (c) 2019 Christian Muehlhaeuser |
| `github.com/rivo/uniseg` | `v0.4.7` | indirect | MIT | Copyright (c) 2019 Oliver Kuederle |
| `github.com/xo/terminfo` | `v0.0.0-20220910002029-abceb7e1c41e` | indirect | MIT | Copyright (c) 2016 Anmol Sethi |
| `golang.org/x/sys` | `v0.50.0` | direct | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `golang.org/x/term` | `v0.48.0` | direct | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `golang.org/x/text` | `v0.3.8` | indirect | BSD-3-Clause | Copyright (c) 2009 The Go Authors. All rights reserved. |

## External runtime dependencies

`ffmpeg` and `ffprobe` are installed separately and are not redistributed with pressurecooker. Their licenses depend on the FFmpeg build installed on the host. Hardware encoders (Vulkan, NVENC, Intel Quick Sync, AMF, and VA-API) use the installed graphics driver.

## License texts

### MIT License

Applies to `pressurecooker` and all MIT-licensed Go modules listed above, with each module's respective copyright notice from the table.

```text
Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF TORT, CONTRACT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### BSD-3-Clause License (Go standard library and runtime, `golang.org/x/sys`, `golang.org/x/term`)

```text
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### BSD-3-Clause License (`golang.org/x/text`)

```text
Copyright (c) 2009 The Go Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google Inc. nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

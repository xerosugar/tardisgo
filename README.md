# TARDIS Go -> Haxe transpiler

#### Haxe -> C++ / C# / Java / JavaScript 

[![Build Status](https://travis-ci.org/tardisgo/tardisgo.png?branch=master)](https://travis-ci.org/tardisgo/tardisgo)
[![GoDoc](https://godoc.org/github.com/tardisgo/tardisgo?status.png)](https://godoc.org/github.com/tardisgo/tardisgo)
[![status](https://sourcegraph.com/api/repos/github.com/tardisgo/tardisgo/badges/status.png)](https://sourcegraph.com/github.com/tardisgo/tardisgo)

## Project status: a working proof of concept
#### EXPERIMENTAL, INCOMPLETE, UN-OPTIMIZED

All of the core [Go language specification](http://golang.org/ref/spec) is implemented, including single-threaded goroutines and channels. However the package "reflect", which is mentioned in the core specification, is not yet fully supported. 

Goroutines are implemented as co-operatively scheduled co-routines. Other goroutines are automatically scheduled every time there is a channel operation or goroutine creation (or call to a function which uses channels or goroutines through any called function). So loops without channel operations may never give up control. The function runtime.Gosched() provides a convenient way to allow other goroutines to run.  

[Well over half of the standard packages pass their tests for at least one target](https://github.com/tardisgo/tardisgo/blob/master/STDPKGSTATUS.md). 

A start has been made on the automated integration with Haxe libraries, but this is incomplete and the API unstable, see the haxe/hx directory and gohaxelib repository for the story so far. 

The code is developed and tested on OS X 10.10.2, using Go 1.4.2 and Haxe 3.2.0-rc.2. The short CI test runs on 64-bit Ubuntu. No other platforms are currently regression tested. 

## Installation and use:
 
Dependencies:
```
go get golang.org/x/tools/go
```
Note, you will see an error: "imports golang.org/x/tools/go: no buildable Go source files in ..."

TARDIS Go:
```
go get -u github.com/tardisgo/tardisgo
```

If tardisgo is not installing and there is a green "build:passing" icon at the top of this page, please e-mail [Elliott](https://github.com/elliott5)!

To translate Go to Haxe, from the directory containing your .go files type the command line: 
```
tardisgo yourfilename.go 
``` 
A large number of .hx files will be created in the tardis subdirectory, of which Go.hx contains the entry-point. The use of a file per Haxe class makes second and subsequent compilations using C++ much faster, as only the altered classes are recompiled.

To run your transpiled code you will first need to install [Haxe](http://haxe.org).

Then to run the tardis/Go.hx file generated above, for example in JavaScript, type the command lines: 
```
haxe -main tardis.Go -cp tardis -js tardis/go.js
node < tardis/go.js
```
... or whatever [Haxe compilation options](http://haxe.org/documentation/introduction/compiler-usage.html) you want to use. 
See the [tgoall.sh](https://github.com/tardisgo/tardisgo-samples/blob/master/scripts/tgoall.sh) script for simple examples.

The default memory model is fast, but requires more memory than you might expect (an int per byte) and only allows some unsafe pointer usages. If your code uses unsafe pointers to re-use memory as different types (say writing a float64 but reading back a uint64), there is a Haxe compilation flag for "fullunsafe" mode (this is slower, but has a smaller memory footprint and allows most unsafe pointers to be modelled accurately). In JS fullunsafe uses the dataview method of object access, for other targets it simulates memory access. Fullunsafe is little-endian only at present and pointer aritmetic (via uintptr) will panic. A command line example: 
```
tardisgo mycode.go
haxe -main tardis.Go -cp tardis -D fullunsafe -js tardis/go-fu.js
node < tardis/go-fu.js
```

While on the subject of JS, the closure compiler seems to work, but not using the "ADVANCED_OPTIMIZATIONS" option.

The in-memory filesystem used by the nacl target is implemented, it can be pre-loaded with files by using the haxe command line flag "-resource" with the name "local/file/path/a.txt@/nacl/file/path/a.txt" thus (for example in JS):
```
tardisgo your_code_using_package_os.go
haxe -main tardis.Go -cp tardis -js tardis/go.js -resource testdata/config.xml@/myapp/static/config.xml
node < tardis/go.js
```
To add more than one file, use multiple -resource flags (the haxe ".hxml" compiler paramater file format can be helpful here). The files are stored as part of the executable code, in a target-specific way. The only resources that will be loaded are those named with a leading "/". A log file of the load process can be found at "/fsinit.log" in the in-memory file-system.

To load a zipped file system (very slow to un-zip, but useful for testing) use go code
`syscall.UnzipFS("myfs.zip")` 
and include 
`-resource myfs.zip` on the haxe command line.

To add Go build tags, use -tags 'name1 name2'. Note that particular Go build tags are required when compiling for OpenFL using the [pre-built Haxe API definitions](https://github.com/tardisgo/gohaxelib). 

Use the "-debug" tardisgo compilation flag to instrument the code and add automated comments to the Haxe. When you experience a panic in this mode the latest Go source code line information and local variables appears in the stack dump. For the C++ & Neko (--interp) targets, a very simple debugger is also available by using the "-D godebug" Haxe flag, for example to use it in C++ type:
```
tardisgo -debug myprogram.go
haxe -main tardis.Go -cp tardis -dce full -D godebug -cpp tardis/cpp
./tardis/cpp/Go
``` 
To get a list of commands type "?" followed by carrage return, after the 1st break location is printed (there is no prompt character). 

To run cross-target command-line tests as quickly as possible, the "-haxe X" flag concurrently runs the Haxe compiler and executes the resulting code as follows:
- "-haxe all" - all supported targets 
- "-haxe math" - only runs C++ and JS with the -D fullunsafe haxe flag (using JS dataview)
- "-haxe interp" - only runs the haxe interpreter (for automated testing, exits with an error if one occurs)
- "-haxe js" - only compiles and runs nodeJS (for automated testing, exits with an error if one occurs)
- "-haxe jsfu" - only compiles (-D fullunsafe) and runs nodeJS (for automated testing, exits with an error if one occurs)
- "-haxe cpp" - only compiles and runs C++ (for automated testing, exits with an error if one occurs)

Compiler output is suppressed and results appear in the order they complete, with an execution time, for example:
```
tardisgo -haxe all myprogram.go
```

When using the -haxe flag with the -test flag, if the file "tgotestfs.zip" exists in the current directory, it will be added as a haxe resource and its contents auto-loaded into the in-memory file system. 

If you can't work-out what is going on prior to a panic, you can add the "-trace" tardisgo compilation flag to instrument the code even further, printing out every part of the code visited. But be warned, the output can be huge.

Please note that strings in Go are held as Haxe strings, but encoded as UTF-8 even when strings for that host are encoded as UTF-16. The system should automatically do the translation to/from the correct format at the Go/Haxe boundary, but there are certain to be some occasions when a translation has to be done explicitly (see Force.toHaxeString/Force.fromHaxeString in haxe/haxeruntime.go).

## Unsupported Haxe targets: ActionScript, PHP, Python and Neko

The nature of ActionScript/Flash means that it is not possible to run automated tests. 

The PHP, Python and Neko targets may work to varying degress, but are not currently reliable enough to permit automated testing. 

PHP specific issues:
* to compile for PHP you currently need to add the haxe compilation option "--php-prefix tgo" to avoid name conflicts
* very long PHP class/file names may cause name resolution problems on some platforms

## Next steps:
Please go to http://github.com/tardisgo/tardisgo-samples for example Go code modified to work with tardisgo. Including some very simple [example code](http://github.com/tardisgo/tardisgo-samples). 

For a small technical FAQ, please see the [Wiki page](https://github.com/tardisgo/tardisgo/wiki). 

For public help or discussion please go to the [Google Group](https://groups.google.com/d/forum/tardisgo); or feel free to e-mail [Elliott](https://github.com/elliott5) direct to discuss any issues if you prefer.

The documentation is sparse at present, if there is some aspect of the system that you want to know more about, please let [Elliott](https://github.com/elliott5) know and he will prioritise that area to add to the wiki.

If you transpile your own code using TARDIS Go, please report the bugs that you find here, so that they can be fixed.

## Why do it at all?:
The objective of this project is to enable the same [Go](http://golang.org) code to be re-deployed in  as many different execution environments as possible, thus saving development time and effort. 
The long-term vision is to provide a framework that makes it easy to target many languages as part of this project.

The first language targeted is [Haxe](http://haxe.org), because the Haxe compiler generates 7 other languages and is already well-proven for making multi-platform client-side applications, mostly games. 

Target short-term use-case is writing multi-platform client-side applications in Go using the APIs available in the Haxe ecosystem, including:
- The standard [Haxe APIs](http://api.haxe.org/) for JavaScript and Flash.
- [OpenFL](http://openfl.org) "Open Flash" to target HTML5, Windows, Mac, Linux, iOS, Android, BlackBerry, Firefox OS, Tizen and Flash, using compiled auto-generated C++ code where possible (star users are TiVo and Prezzi).

Target medium-term use-case will be to make the wider Haxe ecosystem available to Go programmers, including:
- [Flambe](https://github.com/aduros/flambe) cross-platform game engine targeting mobile via Adobe AIR (star users are Disney and Nickelodeon).
- [Kha](http://kha.ktxsoftware.com/) "World's most portable software platform" additionaly targeting Xbox and Playstation.
- Or see a list of many more projects [here](http://old.haxe.org/doc/libraries) (though that page is not exhaustive).

Target long-term use-cases (once the generated code and runtime environment is more efficient):
- For the Haxe community: provide access to the portable elements of Go's extensive libraries and open-source code base.
- For the Go community: write code in Go and call to-and-from existing Haxe, JavaScript, Java or C#  applications (in C++ you would probably just link as normal through CGo). 

For more background and on-line examples see the links from: http://tardisgo.github.io/

## Future plans:

- For all Go standard libraries, [report testing and implementation status](https://github.com/tardisgo/tardisgo/blob/master/STDPKGSTATUS.md)
- Improve integration with Haxe code and libraries, automating as far as possible - [in progress](https://github.com/tardisgo/gohaxelib)
- Improve currently poor execution speeds and update benchmarking results
- Research and publish the best methods to use TARDIS Go to create multi-platform client-side applications - [in progress](https://github.com/tardisgo/tardisgo-samples/tree/master/openfl)
- Improve debug and profiling capabilities
- Add command line flags to control options
- Publish more explanation and documentation
- Move more of the runtime into Go (rather than Haxe) to make it more portable 
- Implement new target languages ;)

If you would like to get involved in helping the project to advance, that would be wonderful. However, please contact [Elliott](https://github.com/elliott5) or discuss your plans in the [tardisgo](https://groups.google.com/d/forum/tardisgo) forum before writing any substantial amounts of code to avoid any conflicts. 

## License:

MIT license, please see the license file.

// Copyright 2014 Elliott Stoneham and The TARDIS Go Authors
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package haxe

import (
	"errors"
	"fmt"
	"go/token"
	"reflect"
	"strings"
	"unicode"

	"github.com/tardisgo/tardisgo/pogo"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/types"
)

var haxeStdSizes = types.StdSizes{
	WordSize: 4, // word size in bytes - must be >= 4 (32bits)
	MaxAlign: 8, // maximum alignment in bytes - must be >= 1
}

func fieldOffset(str *types.Struct, fldNum int) int64 {
	fieldList := make([]*types.Var, str.NumFields())
	for f := 0; f < str.NumFields(); f++ {
		fieldList[f] = str.Field(f)
	}
	return haxeStdSizes.Offsetsof(fieldList)[fldNum]
}

func arrayOffsetCalc(ele types.Type) string {
	ent := types.NewVar(0, nil, "___temp", ele)
	fieldList := []*types.Var{ent, ent}
	off := haxeStdSizes.Offsetsof(fieldList)[1] // to allow for word alignment
	//off := haxeStdSizes.Sizeof(ele) // ?? or should it be the code above ?
	if off == 1 {
		return ""
	}
	for ls := uint(1); ls < 31; ls++ {
		target := int64(1 << ls)
		if off == target {
			return fmt.Sprintf("<<%d", ls)
		}
		if off < target {
			break // no point in looking any further
		}
	}
	return fmt.Sprintf("*%d", off)
}

func emitTrace(s string) string {
	if pogo.TraceFlag {
		return `trace(this._functionName,this._latestBlock,"TRACE ` + s + ` "` /* + ` "+Scheduler.stackDump()` */ + ");\n"
	}
	return ""
}

type langType struct{} // to give us a type to work from when building the interface for pogo

var langIdx int // which entry is this language in pogo.LanguageList

func init() {
	var langVar langType
	var langEntry pogo.LanguageEntry
	langEntry.Language = langVar

	il := 1024 // 1024 is an internal C# limit (`lvregs_len < 1024')

	langEntry.InstructionLimit = il      /* size before we make subfns - java is the most sensitive to this value, was 512 */
	langEntry.SubFnInstructionLimit = il /* 256 required for php, was 256 */
	langEntry.PackageConstVarName = "tardisgoHaxePackage"
	langEntry.HeaderConstVarName = "tardisgoHaxeHeader"
	langEntry.Goruntime = "haxegoruntime" // a string containing the location of the core language runtime functions delivered in Go

	langIdx = len(pogo.LanguageList)
	pogo.LanguageList = append(pogo.LanguageList, langEntry)
}

func (langType) LanguageName() string   { return "haxe" }
func (langType) FileTypeSuffix() string { return ".hx" }

// make a comment
func (langType) Comment(c string) string {
	if c != "" && pogo.DebugFlag { // only comment if something to say and in debug mode
		return " // " + c
	}
	return ""
}

const imports = `` // nothing currently

const tardisgoLicence = `// This code generated using the TARDIS Go tool, elements are
// Copyright 2014 Elliott Stoneham and The TARDIS Go Authors
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file at https://github.com/tardisgo/tardisgo
`

func (langType) FileStart(haxePackageName, headerText string) string {
	if haxePackageName == "" {
		haxePackageName = "tardis"
	}
	return "package " + haxePackageName + ";\n" + imports + headerText + tardisgoLicence
}

// TODO rename
func (langType) FileEnd() string {
	return haxeruntime() // this deals with the individual runtime class files
}

// RegisterName returns the name of an ssa.Value, a utility function in case it needs to be altered.
func (langType) RegisterName(val ssa.Value) string {
	//NOTE the SSA code says that name() should not be relied on, so this code may need to alter
	if useRegisterArray { // we must use a register array when there are too many registers declared at class level for C++/Java to handle
		reg := val.Name()
		if reg[0] != 't' {
			panic("Register Name does not begin with t: " + reg)
		}
		return "_t[" + reg[1:] + "]"
	} else {
		return "_" + val.Name()
	}
}

var useRegisterArray bool // should we use an array rather than individual register vars

var nextReturnAddress int       // what number is the next pseudo block return address?
var hadReturn bool              // has there been a return statement in this function?
var hadBlockReturn bool         // has there been a return in this block?
var pseudoNextReturnAddress int // what is the next pseudo block to emit/or limit of what's been emitted
var pseudoBlockNext int         // what is the next pseudo block we should have emitted?
var currentfn *ssa.Function     // what we are currently working on
var currentfnName string        // the Haxe name of what we are currently working on
var fnUsesGr bool               // does the current function use Goroutines?

func (l langType) FuncStart(packageName, objectName string, fn *ssa.Function, position string, isPublic, trackPhi, usesGr bool, canOptMap map[string]bool) string {

	//fmt.Println("DEBUG: HAXE FuncStart: ", packageName, ".", objectName, usesGr)

	nextReturnAddress = -1
	hadReturn = false
	hadBlockReturn = false
	pseudoBlockNext = -1
	currentfn = fn
	currentfnName = "Go_" + l.LangName(packageName, objectName)
	fnUsesGr = usesGr

	ret := ""

	// need to make private classes, aside from correctness,
	// because cpp & java have a problem with functions whose names are the same except for the case of the 1st letter
	if isPublic {
		ret += fmt.Sprintf(`#if js @:expose("Go_%s") #end `, l.LangName(packageName, objectName))
	} else {
		//	ret += "#if (!php) private #end " // for some reason making classes private is a problem in php
	}
	ret += fmt.Sprintf("class %s extends StackFrameBasis implements StackFrame { %s\n",
		currentfnName, l.Comment(position))

	//Create the stack frame variables
	hadBlank := false
	for p := range fn.Params {
		prefix := "p_"
		if hadBlank && fn.Params[p].Name() == "_" {
			prefix += fmt.Sprintf("%d", p)
		}
		ret += "private var " + prefix + pogo.MakeID(fn.Params[p].Name()) + ":" + l.LangType(fn.Params[p].Type(), /*.Underlying()*/
			false, fn.Params[p].Name()+position) + ";\n"
		if fn.Params[p].Name() == "_" {
			hadBlank = true
		}
	}
	ret += "public function new(gr:Int,"
	ret += "_bds:Dynamic" //bindings
	for p := range fn.Params {
		ret += ", "
		ret += "p_" + pogo.MakeID(fn.Params[p].Name()) + " : " + l.LangType(fn.Params[p].Type() /*.Underlying()*/, false, fn.Params[p].Name()+position)
	}
	ret += ") {\nsuper(gr," + fmt.Sprintf("%d", pogo.LatestValidPosHash) + ",\"Go_" + l.LangName(packageName, objectName) + "\");\nthis._bds=_bds;\n"
	hadBlank = false
	for p := range fn.Params {
		prefix := "this.p_"
		if hadBlank && fn.Params[p].Name() == "_" {
			prefix += fmt.Sprintf("%d", p)
		}
		ret += prefix + pogo.MakeID(fn.Params[p].Name()) + "=p_" + pogo.MakeID(fn.Params[p].Name()) + ";\n"
		if pogo.DebugFlag {
			ret += `this._debugVars.set("` + fn.Params[p].Name() + `",p_` + pogo.MakeID(fn.Params[p].Name()) + ");\n"
		}
		if fn.Params[p].Name() == "_" {
			hadBlank = true
		}
	}
	if fn.Recover != nil {
		for b := 0; b < len(fn.Blocks); b++ {
			if fn.Recover == fn.Blocks[b] {
				ret += fmt.Sprintf("this._recoverNext=%d;\n", b)
				break
			}
		}
	}
	ret += emitTrace(`New:` + l.LangName(packageName, objectName))
	ret += "Scheduler.push(gr,this);\n}\n"

	rTyp := ""
	switch fn.Signature.Results().Len() {
	case 0:
		// NoOp
	case 1:
		rTyp = l.LangType(fn.Signature.Results().At(0).Type() /*.Underlying()*/, false, position)
	default:
		rTyp = "{"
		for r := 0; r < fn.Signature.Results().Len(); r++ {
			if r != 0 {
				rTyp += ", "
			}
			rTyp += fmt.Sprintf("r%d:", r) + l.LangType(fn.Signature.Results().At(r).Type() /*.Underlying()*/, false, position)
		}
		rTyp += "}"
	}
	if rTyp != "" {
		ret += "private var _res:" + rTyp + ";\n"
		ret += "public inline function res():Dynamic " + "{return _res;}\n"
	} else {
		ret += "public inline function res():Dynamic {return null;}\n" // just to keep the interface definition happy
	}

	// call from haxe (TODO: maybe run in a new goroutine)
	ret += "public static function hx( " // used to call this function from Haxe
	for p := range fn.Params {
		if p != 0 {
			ret += ", "
		}
		ret += "p_" + pogo.MakeID(fn.Params[p].Name()) + " : " + l.LangType(fn.Params[p].Type() /*.Underlying()*/, false, fn.Params[p].Name()+position)
	}
	ret += ") : "
	switch fn.Signature.Results().Len() {
	case 0:
		ret += "Void"
	case 1:
		ret += l.LangType(fn.Signature.Results().At(0).Type() /*.Underlying()*/, false, position)
	default:
		ret += "{"
		for r := 0; r < fn.Signature.Results().Len(); r++ {
			if r != 0 {
				ret += ", "
			}
			ret += fmt.Sprintf("r%d:", r) + l.LangType(fn.Signature.Results().At(r).Type() /*.Underlying()*/, false, position)
		}
		ret += "}"
	}
	ret += " {\n"
	ret += "if(!Go.doneInit) Go.init();\n" // very defensive TODO remove this once everyone understands that Go.init() must be called first
	ret += "var _sf=new Go_" + l.LangName(packageName, objectName)
	ret += "(0,null" // NOTE calls from Haxe hijack goroutine 0, so the main go goroutine will be suspended for the duration
	for p := range fn.Params {
		ret += ", "
		if fn.Params[p].Type().Underlying().String() == "string" {
			ret += "Force.fromHaxeString("
		}
		ret += "p_" + pogo.MakeID(fn.Params[p].Name())
		if fn.Params[p].Type().Underlying().String() == "string" {
			ret += ")"
		}
	}
	ret += ").run(); \n"
	if usesGr {
		ret += "while(_sf._incomplete) Scheduler.runAll();\n" // TODO alter for multi-threading if ever implemented
	}
	if fn.Signature.Results().Len() > 0 {
		if fn.Signature.Results().Len() == 1 {
			if fn.Signature.Results().At(0).Type().Underlying().String() == "string" {
				ret += "return Force.toHaxeString(cast(_sf.res(),String));\n"
			} else {
				ret += "return _sf.res();\n"
			}
		} else {
			ret += "var _r = _sf.res();\n"
			for rv := 0; rv < fn.Signature.Results().Len(); rv++ {
				if fn.Signature.Results().At(rv).Type().Underlying().String() == "string" {
					ret += fmt.Sprintf("_r.r%d = Force.toHaxeString(cast(_r.r%d,String));\n", rv, rv)
				}
			}
			ret += "return _r;\n"
		}
	}
	ret += "}\n"

	// call from haxe go runtime - use current goroutine
	ret += "public static function callFromRT( _gr:Int"
	for p := range fn.Params {
		//if p != 0 {
		ret += ", "
		//}
		ret += "p_" + pogo.MakeID(fn.Params[p].Name()) + " : " + l.LangType(fn.Params[p].Type() /*.Underlying()*/, false, fn.Params[p].Name()+position)
	}
	ret += ") : "
	switch fn.Signature.Results().Len() {
	case 0:
		ret += "Void"
	case 1:
		ret += l.LangType(fn.Signature.Results().At(0).Type() /*.Underlying()*/, false, position)
	default:
		ret += "{"
		for r := 0; r < fn.Signature.Results().Len(); r++ {
			if r != 0 {
				ret += ", "
			}
			ret += fmt.Sprintf("r%d:", r) + l.LangType(fn.Signature.Results().At(r).Type() /*.Underlying()*/, false, position)
		}
		ret += "}"
	}
	ret += " {\n" /// we have already done Go.init() if we are calling from the runtime
	ret += "var _sf=new Go_" + l.LangName(packageName, objectName)
	ret += "(_gr,null" //  use the given Goroutine
	for p := range fn.Params {
		ret += ", "
		ret += "p_" + pogo.MakeID(fn.Params[p].Name())
	}
	ret += ").run(); \n"
	if usesGr {
		ret += "while(_sf._incomplete) Scheduler.run1(_gr);\n" // NOTE no "panic()" or "go" code in runtime Go
	}
	if fn.Signature.Results().Len() > 0 {
		ret += "return _sf.res();\n"
	}
	ret += "}\n"

	// call
	ret += "public static function call( gr:Int," //this just creates the stack frame, NOTE does not run anything because also used for defer
	ret += "_bds:Dynamic"                         //bindings
	for p := range fn.Params {
		ret += ", "
		ret += "p_" + pogo.MakeID(fn.Params[p].Name()) + " : " + l.LangType(fn.Params[p].Type() /*.Underlying()*/, false, fn.Params[p].Name()+position)
	}
	ret += ") : Go_" + l.LangName(packageName, objectName)
	ret += "\n{" + ""
	ret += "return "
	ret += "new Go_" + l.LangName(packageName, objectName) + "(gr,_bds"
	for p := range fn.Params {
		ret += ", "
		ret += "p_" + pogo.MakeID(fn.Params[p].Name())
	}
	ret += ");\n"
	ret += "}\n"

	if !usesGr {
		ret += l.runFunctionCode(packageName, objectName, "[ OPTIMIZED NON-GOROUTINE FUNCTION ]")
	}

	regCount := 0
	regDefs := ""
	useRegisterArray = false

	pseudoNextReturnAddress = -1
	for b := range fn.Blocks {
		for i := range fn.Blocks[b].Instrs {
			in := fn.Blocks[b].Instrs[i]
			reg := l.Value(in, pogo.CodePosition(in.Pos()))

			switch in.(type) {
			case *ssa.Call:
				switch in.(*ssa.Call).Call.Value.(type) {
				case *ssa.Builtin:
					//NoOp
				default:
					// Optimise here not to declare Stack Frames for pseudo-functions used when calling Haxe code direct
					pp := getPackagePath(in.(*ssa.Call).Common())
					ppBits := strings.Split(pp, "/")
					if ppBits[len(ppBits)-1] != "hx" && !strings.HasPrefix(ppBits[len(ppBits)-1], "_") {
						//if usesGr {
						//	ret += "private "
						//}
						ret += fmt.Sprintf("var _SF%d:StackFrame", -pseudoNextReturnAddress) //TODO set correct type, or let Haxe determine
						if usesGr {
							ret += " #if js =null #end ;\n"
						} else {
							ret += "=null;\n" // need to initalize when using the native stack for these vars
						}
					}
					pseudoNextReturnAddress--
				}
			case *ssa.Send, *ssa.Select, *ssa.RunDefers, *ssa.Panic:
				pseudoNextReturnAddress--
			case *ssa.UnOp:
				if in.(*ssa.UnOp).Op == token.ARROW {
					pseudoNextReturnAddress--
				}
			case *ssa.Alloc:
				if !in.(*ssa.Alloc).Heap { // allocate space on the stack if possible
					//fmt.Println("DEBUG allocate stack space for", reg, "at", position)
					if reg != "" {
						ret += haxeVar(reg+"_stackalloc", "Object", "="+allocNewObject(in.(*ssa.Alloc).Type()), position, "FuncStart()") + "\n"
					}
				}
			}

			if reg != "" && !canOptMap[reg[1:]] { // only add the reg to the SF if not defined in sub-functions
				// Underlying() not used in 2 lines below because of *ssa.(opaque type)
				typ := l.LangType(in.(ssa.Value).Type(), false, reg+"@"+position)
				init := l.LangType(in.(ssa.Value).Type(), true, reg+"@"+position) // this may be overkill...

				if strings.HasPrefix(init, "{") || strings.HasPrefix(init, "new Pointer") ||
					//strings.HasPrefix(init, "new UnsafePointer") ||
					strings.HasPrefix(init, "new Object") || strings.HasPrefix(init, "new Slice") ||
					strings.HasPrefix(init, "new Chan") || strings.HasPrefix(init, "new GOmap") ||
					strings.HasPrefix(init, "new Complex") || strings.HasPrefix(init, "GOint64.make") { // stop unnecessary initialisation
					// all SSA registers are actually assigned to before use, so minimal initialisation is required, except for maps
					init = "null"
				}
				if typ != "" {
					switch len(*in.(ssa.Value).Referrers()) {
					case 0: // don't allocate unused temporary variables
					//case 1: // TODO optimization possible using register replacement but does not currenty work for: a,b=b,a+b, so code removed
					default:
						if usesGr {
							if init == "null" {
								init = "" // in JS null is the default anyway
							} else {
								init = " #if js =" + init + " #end " // only init in JS, to tell the var type for v8 opt
							}
						} else {
							init = "=" + init // when not using goroutines, they all need initializing
						}
						//if usesGr {
						//	ret += "private "
						//}
						hv := haxeVar(reg, typ, init, position, "FuncStart()") + "\n"
						//if usesGr {
						//	if strings.Contains(hv, ":") {
						//		hv = strings.Replace(hv, ":", "(null,null):", 1)
						//	}
						//}
						regDefs += hv
						regCount++
					}
				}
			}
		}
	}

	if regCount > pogo.LanguageList[langIdx].InstructionLimit { // should only affect very large init() fns
		fmt.Println("DEBUG regCount", currentfnName, regCount)
		useRegisterArray = true
		ret += "var _t=new Array<Dynamic>();\n"
	} else {
		useRegisterArray = false
		ret += regDefs
	}
	//TODO optimise (again) for if only one block (as below) AND no calls (which create synthetic values for _Next)
	//if len(fn.Blocks) > 1 { // if there is only one block then we don't need to track which one is next
	if trackPhi {
		ret += "var _Phi:Int=0;\n"
	}
	//ret += "_Next=0;\n" // now defined in the interface StackFrame and StackFrameBasis
	//}

	if usesGr {
		ret += l.runFunctionCode(packageName, objectName, "")
	}

	//}
	//TODO optimise (again) for if only one block (as below) AND no calls (which create synthetic values for _Next)
	//if len(fn.Blocks) > 1 { // if there is only one block then we don't need to track which one is next
	//ret += "while(true){\nswitch(_Next) {"
	//}

	return ret
}

func (l langType) runFunctionCode(packageName, objectName, msg string) string {
	ret := "public function run():Go_" + l.LangName(packageName, objectName) + " { //" + msg + "\n"
	ret += emitTrace(`Run: ` + l.LangName(packageName, objectName) + " " + msg)
	return ret
}

func (l langType) whileCaseCode() string {
	// NOTE this rather odd arrangement improves JS V8 optimization
	ret := "#if js\n"
	ret += "\tvar retVal:" + currentfnName + "=null;\n"
	ret += "\twhile(retVal==null) \n\t\tswitch(_Next){\n"
	for b := range currentfn.Blocks {
		ret += fmt.Sprintf("\t\tcase %d: retVal=_Block%d();\n", b, b)
	}
	for p := -1; p > pseudoNextReturnAddress; p-- {
		ret += fmt.Sprintf("\t\tcase %d: retVal=_Block_%d();\n", p, -p)
	}
	ret += "\t\tdefault: Scheduler.bbi();\n"
	ret += "\t\t}\n\treturn retVal;\n"
	ret += "#else\n"
	ret += "\tdefault: Scheduler.bbi();\n}\n"
	ret += "#end\n"
	return ret
}

func (l langType) RunEnd(fn *ssa.Function) string {
	// TODO reoptimize if blocks >0 and no calls that create synthetic block entries
	/*
		ret := ""
		if len(fn.Blocks) == 1 && !hadReturn {
			ret += l.Ret(nil, "") // required because sometimes the SSA code is not generated for this
		}
		return ret + `default: Scheduler.bbi();}}}`
	*/
	ret := emitUnseenPseudoBlocks()
	ret += l.whileCaseCode()
	return ret + "\n}\n"
}
func (l langType) FuncEnd(fn *ssa.Function) string {
	// actually, the end of the class for that Go function
	pogo.WriteAsClass(currentfnName, "}\n")
	return ``
}

// utiltiy to set-up a haxe variable
func haxeVar(reg, typ, init, position, errorStart string) string {
	if typ == "" {
		pogo.LogError(position, "Haxe", fmt.Errorf(errorStart+" unhandled initialisation for empty type"))
		return ""
	}
	ret := "var " + reg + ":" + typ
	if init != "" {
		ret += init
	}
	return ret + ";"
}

func (l langType) SetPosHash() string {
	return "this.setPH(" + fmt.Sprintf("%d", pogo.LatestValidPosHash) + ");"
}

func (l langType) BlockStart(block []*ssa.BasicBlock, num int, emitPhi bool) string {
	hadBlockReturn = false
	// TODO optimise is only 1 block AND no calls
	// TODO if len(block) > 1 { // no need for a case statement if only one block
	ret := ""
	if num == 0 {
		ret += "#if !js while(true)switch(_Next){ #end\n"
	}
	ret += fmt.Sprintf("#if !js case %d: #end", num) + l.Comment(block[num].Comment) + "\n"
	ret += fmt.Sprintf("#if js function _Block%d(){ #end\n", num)
	ret += emitTrace(fmt.Sprintf("Function: %s Block:%d", block[num].Parent(), num))
	if pogo.DebugFlag {
		ret += "this.setLatest(" + fmt.Sprintf("%d", pogo.LatestValidPosHash) + "," + fmt.Sprintf("%d", num) + ");\n"
	}
	return ret
}

func (l langType) BlockEnd(block []*ssa.BasicBlock, num int, emitPhi bool) string {
	ret := ""
	if emitPhi {
		ret += fmt.Sprintf(" _Phi=%d;\n", num)
	}
	if !hadBlockReturn {
		ret += "#if js return null; #end\n"
	}
	hadBlockReturn = true
	ret += "#if js } #end\n"
	return ret
}

func (l langType) Jump(block int) string {
	return fmt.Sprintf("_Next=%d;", block)
}

func (l langType) If(v interface{}, trueNext, falseNext int, errorInfo string) string {
	return fmt.Sprintf("_Next=%s ? %d : %d;", l.IndirectValue(v, errorInfo), trueNext, falseNext)
}

func (l langType) Phi(register string, phiEntries []int, valEntries []interface{}, defaultValue, errorInfo string) string {
	ret := register + "=("
	for e := range phiEntries {
		val := l.IndirectValue(valEntries[e], errorInfo)
		ret += fmt.Sprintf("(_Phi==%d)?%s:", phiEntries[e], val)
	}
	return ret + defaultValue + ");"
}

func (l langType) LangName(p, o string) string {
	return pogo.MakeID(p) + "_" + pogo.MakeID(o)
}

// Returns the textual version of Value, possibly emmitting an error
// can't merge with indirectValue, as this is used by emit-func-setup to get register names
func (l langType) Value(v interface{}, errorInfo string) string {
	val, ok := v.(ssa.Value)
	if !ok {
		return "" // if it is not a value, an empty string will be returned
	}
	switch v.(type) {
	case *ssa.Global:
		return "Go." + l.LangName(v.(*ssa.Global).Pkg.Object.Path() /* was .Name()*/, v.(*ssa.Global).Name())
	case *ssa.Alloc, *ssa.MakeSlice:
		return pogo.RegisterName(v.(ssa.Value))
	case *ssa.FieldAddr, *ssa.IndexAddr:
		return pogo.RegisterName(v.(ssa.Value))
	case *ssa.Const:
		ci := v.(*ssa.Const)
		_, c := l.Const(*ci, errorInfo)
		return c
	case *ssa.Parameter:
		return "p_" + pogo.MakeID(v.(*ssa.Parameter).Name())
	//case *ssa.Capture:
	//	for b := range v.(*ssa.Capture).Parent().FreeVars {
	//		if v.(*ssa.Capture) == v.(*ssa.Capture).Parent().FreeVars[b] { // comparing the name gives the wrong result
	//			return `_bds[` + fmt.Sprintf("%d", b) + `]`
	//		}
	//	}
	//	pogo.LogError(errorInfo, "Haxe", fmt.Errorf("haxe.Value(): *ssa.Capture name not found: %s", v.(*ssa.Capture).Name()))
	//	return `_bds["_b` + "ERROR: Captured bound variable name not found" + `"]` // TODO proper error
	case *ssa.FreeVar:
		return `_bds.` + v.(*ssa.FreeVar).Name()
	case *ssa.Function:
		pk, _ := pogo.FuncPathName(v.(*ssa.Function))  //fmt.Sprintf("fn%d", v.(*ssa.Function).Pos())
		if v.(*ssa.Function).Signature.Recv() != nil { // it's a method
			pn := v.(*ssa.Function).Signature.Recv().Pkg().Path() // was .Name()
			pk = pn + "." + v.(*ssa.Function).Signature.Recv().Name()
		} else {
			if v.(*ssa.Function).Pkg != nil {
				if v.(*ssa.Function).Pkg.Object != nil {
					pk = v.(*ssa.Function).Pkg.Object.Path() // was .Name()
				}
			}
		}
		if len(v.(*ssa.Function).Blocks) > 0 { //the function actually exists
			return "new Closure(Go_" + l.LangName(pk, v.(*ssa.Function).Name()) + ".call,null)" //TODO will change for go instr
		}
		// function has no implementation
		// TODO maybe put a list of over-loaded functions here and only error if not found
		// NOTE the reflect package comes through this path TODO fix!
		pogo.LogWarning(errorInfo, "Haxe", fmt.Errorf("haxe.Value(): *ssa.Function has no implementation: %s", v.(*ssa.Function).Name()))
		return "new Closure(null,null)" // Should fail at runtime if it is used...
	case *ssa.UnOp:
		return pogo.RegisterName(val)
	case *ssa.BinOp:
		return pogo.RegisterName(val)
	case *ssa.MakeInterface:
		return pogo.RegisterName(val)
	default:
		return pogo.RegisterName(val)
	}
}
func (l langType) FieldAddr(register string, v interface{}, errorInfo string) string {
	if register != "" {
		ptr := l.IndirectValue(v.(*ssa.FieldAddr).X, errorInfo)
		ptr = "Pointer.check(" + ptr + ")"
		fld := v.(*ssa.FieldAddr).X.Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Struct).Field(v.(*ssa.FieldAddr).Field)
		off := fieldOffset(v.(*ssa.FieldAddr).X.Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Struct), v.(*ssa.FieldAddr).Field)
		return fmt.Sprintf(`%s=%s.fieldAddr( /*%d : %s */ %d );`, register,
			ptr, v.(*ssa.FieldAddr).Field, fixKeyWds(fld.Name()), off)
	}
	return ""
}

func wrapForce_toUInt(v string, k types.BasicKind) string {
	switch k {
	case types.Uintptr:
		return "Force.toUint32(Force.toInt(" + v + "))"
	case types.Int64, types.Uint64:
		return "Force.toUint32(GOint64.toInt(" + v + "))"
	case types.Float32, types.Float64, types.UntypedFloat:
		return "Force.toUint32(" + v + "<=0?Math.ceil(" + v + "):Math.floor(" + v + "))"
	}
	return v
}

func (l langType) IndexAddr(register string, v interface{}, errorInfo string) string {
	if register == "" {
		return "" // we can't make an address if there is nowhere to put it...
	}
	idxString := wrapForce_toUInt(l.IndirectValue(v.(*ssa.IndexAddr).Index, errorInfo),
		v.(*ssa.IndexAddr).Index.(ssa.Value).Type().Underlying().(*types.Basic).Kind())
	switch v.(*ssa.IndexAddr).X.Type().Underlying().(type) {
	case *types.Pointer:
		ptr := l.IndirectValue(v.(*ssa.IndexAddr).X, errorInfo)
		ptr = "Pointer.check(" + ptr + ")"
		ele := v.(*ssa.IndexAddr).X.Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Array).Elem().Underlying()
		return fmt.Sprintf(`%s=%s.addr(%s%s);`, register,
			ptr, idxString, arrayOffsetCalc(ele))
	case *types.Slice:
		return fmt.Sprintf(`%s=%s.itemAddr(%s);`, register,
			l.IndirectValue(v.(*ssa.IndexAddr).X, errorInfo),
			idxString)
	//case *types.Array: // need to create a pointer before using it
	//	ele := v.(*ssa.IndexAddr).X.Type().Underlying().(*types.Array).Elem().Underlying()
	//	return fmt.Sprintf(`%s={var _v=new Pointer<%s>(%s); _v.addr(%s%s);};`, register, // TODO is Pointer<type> correct?
	//		l.LangType(v.(*ssa.IndexAddr).X.Type().Underlying().(*types.Array).Elem().Underlying(), false, errorInfo),
	//		l.IndirectValue(v.(*ssa.IndexAddr).X, errorInfo),
	//		idxString, arrayOffsetCalc(ele))
	default:
		pogo.LogError(errorInfo, "Haxe", fmt.Errorf("haxe.IndirectValue():IndexAddr unknown operand type"))
		return ""
	}
}

func (l langType) IndirectValue(v interface{}, errorInfo string) string {
	return l.Value(v, errorInfo)
}

func (l langType) intTypeCoersion(t types.Type, v, errorInfo string) string {
	switch t.Underlying().(type) {
	case *types.Basic:
		switch t.Underlying().(*types.Basic).Kind() {
		case types.Int8:
			return "Force.toInt8(" + v + ")"
		case types.Int16:
			return "Force.toInt16(" + v + ")"
		case types.Int32, types.Int: // NOTE type int is always int32
			return "Force.toInt32(" + v + ")"
		case types.Int64:
			return "Force.toInt64(" + v + ")"
		case types.Uint8:
			return "Force.toUint8(" + v + ")"
		case types.Uint16:
			return "Force.toUint16(" + v + ")"
		case types.Uint32, types.Uint, types.Uintptr: // NOTE type uint is always uint32
			return "Force.toUint32(" + v + ")"
		case types.Uint64:
			return "Force.toUint64(" + v + ")"
		case types.UntypedInt, types.UntypedRune:
			pogo.LogError(errorInfo, "Haxe", fmt.Errorf("haxe.intTypeCoersion(): unhandled types.UntypedInt or types.UntypedRune"))
			return ""
		case types.Float32:
			return "Force.toFloat32(" + v + ")"
		case types.Float64, types.Bool:
			return v
		default:
			pogo.LogError(errorInfo, "Haxe", fmt.Errorf("haxe.intTypeCoersion():unhandled basic kind %s",
				t.Underlying().(*types.Basic).Kind()))
			return v
		}
	default:
		pogo.LogError(errorInfo, "Haxe", fmt.Errorf("haxe.intTypeCoersion():unhandled type %T", t.Underlying()))
		return v
	}
}

func (l langType) Store(v1, v2 interface{}, errorInfo string) string {
	ptr := l.IndirectValue(v1, errorInfo)
	ptr = "Pointer.check(" + ptr + ")"
	return ptr + ".store" + loadStoreSuffix(v2.(ssa.Value).Type().Underlying(), true) +
		l.IndirectValue(v2, errorInfo) + ");" +
		" /* " + v2.(ssa.Value).Type().Underlying().String() + " */ "
}

func (l langType) Send(v1, v2 interface{}, errorInfo string) string {
	ret := fmt.Sprintf("_Next=%d;\n", nextReturnAddress)
	ret += "return this;\n"
	ret += "#if js } #end\n"
	ret += emitUnseenPseudoBlocks()
	ret += fmt.Sprintf("#if !js case %d: #end\n", nextReturnAddress)
	ret += fmt.Sprintf("#if js function _Block_%d(){ #end\n", -nextReturnAddress)
	if pogo.DebugFlag {
		ret += "this.setLatest(" + fmt.Sprintf("%d", pogo.LatestValidPosHash) + "," + fmt.Sprintf("%d", nextReturnAddress) + ");\n"
	}
	ret += emitTrace(fmt.Sprintf("Block:%d", nextReturnAddress))
	// TODO panic if the chanel is null
	ret += "if(!" + l.IndirectValue(v1, errorInfo) + ".hasSpace())return this;\n" // go round the loop again and wait if not OK
	ret += l.IndirectValue(v1, errorInfo) + ".send(" + l.IndirectValue(v2, errorInfo) + ");"
	nextReturnAddress-- // decrement to set new return address for next code generation
	hadBlockReturn = false
	return ret
}

func emitReturnHere() string {
	ret := ""
	ret += fmt.Sprintf("_Next=%d;\n", nextReturnAddress)
	ret += "return this;\n"
	ret += "#if js } #end\n"
	ret += emitUnseenPseudoBlocks()
	ret += fmt.Sprintf("#if !js case %d: #end\n", nextReturnAddress)
	ret += fmt.Sprintf("#if js function _Block_%d(){ #end\n", -nextReturnAddress)
	if pogo.DebugFlag {
		ret += "this.setLatest(" + fmt.Sprintf("%d", pogo.LatestValidPosHash) + "," + fmt.Sprintf("%d", nextReturnAddress) + ");\n"
	}
	ret += emitTrace(fmt.Sprintf("Block:%d", nextReturnAddress))
	hadBlockReturn = false
	return ret
}

func emitUnseenPseudoBlocks() string {
	ret := ""
	if nextReturnAddress == pseudoBlockNext {
		pseudoBlockNext = nextReturnAddress - 1
		return ret
	}
	// we've missed some
	for pseudoBlockNext > nextReturnAddress {
		ret += fmt.Sprintf("#if js function _Block_%d():Dynamic{return null;} #end\n", -pseudoBlockNext)
		pseudoBlockNext--
	}
	pseudoBlockNext = nextReturnAddress - 1
	return ret
}

// if isSelect is false, v is the UnOp value, otherwise the ssa.Select instruction
/* SSA DOCUMENTAION EXTRACT
The Select instruction tests whether (or blocks until) one or more of the specified sent or received states is entered.

Let n be the number of States for which Dir==RECV and T_i (0<=i<n) be the element type of each such state's Chan.
Select returns an n+2-tuple

(index int, recvOk bool, r_0 T_0, ... r_n-1 T_n-1)
The tuple's components, described below, must be accessed via the Extract instruction.

If Blocking, select waits until exactly one state holds, i.e. a channel becomes ready for the designated operation
of sending or receiving; select chooses one among the ready states pseudorandomly, performs the send or receive operation,
and sets 'index' to the index of the chosen channel.

If !Blocking, select doesn't block if no states hold; instead it returns immediately with index equal to -1.

If the chosen channel was used for a receive, the r_i component is set to the received value,
where i is the index of that state among all n receive states; otherwise r_i has the zero value of type T_i.
Note that the the receive index i is not the same as the state index index.

The second component of the triple, recvOk, is a boolean whose value is true iff
the selected operation was a receive and the receive successfully yielded a value.
*/
func (l langType) Select(isSelect bool, register string, v interface{}, CommaOK bool, errorInfo string) string {
	ret := emitReturnHere() // even if we are in a non-blocking select, we need to give the other goroutines a chance!
	if isSelect {
		sel := v.(*ssa.Select)
		if register == "" {
			pogo.LogError(errorInfo, "Haxe", fmt.Errorf("select statement has no register"))
			return ""
		}
		ret += register + "=" + l.LangType(v.(ssa.Value).Type(), true, errorInfo) + ";\n" //initialize
		ret += register + ".r0= -1;\n"                                                    // the returned index if nothing is found

		if len(sel.States) > 0 { // only do the logic if there are states to choose between
			// TODO a blocking select with no states could be further optimised to stop the goroutine

			// Spec requires a pseudo-random order to which item is processed
			ret += fmt.Sprintf("{ var _states:Array<Bool> = new Array(); var _rnd=Std.random(%d);\n", len(sel.States))
			for s := range sel.States {
				switch sel.States[s].Dir {
				case types.SendOnly:
					ch := l.IndirectValue(sel.States[s].Chan, errorInfo)
					ret += fmt.Sprintf("_states[%d]=%s==null?false:%s.hasSpace();\n", s, ch, ch)
				case types.RecvOnly:
					ch := l.IndirectValue(sel.States[s].Chan, errorInfo)
					ret += fmt.Sprintf("_states[%d]=%s==null?false:%s.hasContents();\n", s, ch, ch)
				default:
					pogo.LogError(errorInfo, "Haxe", fmt.Errorf("select statement has invalid ChanDir"))
					return ""
				}
			}
			ret += fmt.Sprintf("for(_s in 0...%d) {var _i=(_s+_rnd)%s%d; if(_states[_i]) {%s.r0=_i; break;};}\n",
				len(sel.States), "%", len(sel.States), register)
			ret += fmt.Sprintf("switch(%s.r0){", register)
			rxIdx := 0
			for s := range sel.States {
				ret += fmt.Sprintf("case %d:\n", s)
				switch sel.States[s].Dir {
				case types.SendOnly:
					ch := l.IndirectValue(sel.States[s].Chan, errorInfo)
					snd := l.IndirectValue(sel.States[s].Send, errorInfo)
					ret += fmt.Sprintf("%s.send(%s);\n", ch, snd)
				case types.RecvOnly:
					ch := l.IndirectValue(sel.States[s].Chan, errorInfo)
					ret += fmt.Sprintf("{ var _v=%s.receive(%s); ", ch,
						l.LangType(sel.States[s].Chan.(ssa.Value).Type().Underlying().(*types.Chan).Elem().Underlying(), true, errorInfo))
					ret += fmt.Sprintf("%s.r%d= _v.r0; ", register, 2+rxIdx)
					rxIdx++
					ret += register + ".r1= _v.r1; }\n"
				default:
					pogo.LogError(errorInfo, "Haxe", fmt.Errorf("select statement has invalid ChanDir"))
					return ""
				}
			}
			ret += "};}\n" // end switch; _states, _rnd scope

		} // end only if len(sel.States)>0

		if sel.Blocking {
			ret += "if(" + register + ".r0 == -1) return this;\n"
		}

	} else {
		ret += "if(" + l.IndirectValue(v, errorInfo) + ".hasNoContents())return this;\n" // go round the loop again and wait if not OK
		if register != "" {
			ret += register + "="
		}
		ret += l.IndirectValue(v, errorInfo) + ".receive("
		ret += l.LangType(v.(ssa.Value).Type().Underlying().(*types.Chan).Elem().Underlying(), true, errorInfo) + ")" // put correct result into register
		if !CommaOK {
			ret += ".r0"
		}
		ret += ";"
	}
	nextReturnAddress-- // decrement to set new return address for next code generation
	return ret
}
func (l langType) RegEq(r string) string {
	return r + "="
}

func (l langType) Ret(values []*ssa.Value, errorInfo string) string {
	hadReturn = true
	_BlockEnd := "this._incomplete=false;\nScheduler.pop(this._goroutine);\n"
	hadBlockReturn = true
	_BlockEnd += "return this;\n"
	switch len(values) {
	case 0:
		return emitTrace("Ret0") + _BlockEnd
	case 1:
		return emitTrace("Ret1") + "_res= " + l.IndirectValue(*values[0], errorInfo) + ";\n" + _BlockEnd
	default:
		ret := emitTrace("RetN") + "_res= {"
		for r := range values {
			if r != 0 {
				ret += ","
			}
			if l.LangType((*values[r]).Type().Underlying(), false, errorInfo) == "GOint64" {
				ret += fmt.Sprintf("r%d:", r) + l.IndirectValue(*values[r], errorInfo)
			} else {
				ret += fmt.Sprintf("r%d:", r) + l.IndirectValue(*values[r], errorInfo)
			}
		}
		return ret + "};\n" + _BlockEnd
	}
}

func (l langType) Panic(v1 interface{}, errorInfo string, usesGr bool) string {
	ret := l.doCall("", nil, "Scheduler.panic(this._goroutine,"+l.IndirectValue(v1, errorInfo)+");\n", usesGr)
	ret += l.Ret(nil, errorInfo) // just in case we return to this point without _recoverNext being set & used
	return ret
}

func getPackagePath(cc *ssa.CallCommon) string {
	// This code to find the package name
	var pn string = "UNKNOWN" // package name
	if cc.StaticCallee() != nil {
		pn, _ = pogo.FuncPathName(cc.StaticCallee()) // was =fmt.Sprintf("fn%d", cc.StaticCallee().Pos())
	}
	if cc != nil {
		if cc.Method != nil {
			if cc.Method.Pkg() != nil {
				pn = cc.Method.Pkg().Path() // was .Name()
			}
		} else {
			if cc.StaticCallee() != nil {
				if cc.StaticCallee().Package() != nil {
					pn = cc.StaticCallee().Package().String()
				} else {
					if cc.StaticCallee().Object() != nil {
						if cc.StaticCallee().Object().Pkg() != nil {
							pn = cc.StaticCallee().Object().Pkg().Path() // was .Name()
						}
					}
				}
			}
		}
	}
	return pn
}

func (l langType) Call(register string, cc ssa.CallCommon, args []ssa.Value, isBuiltin, isGo, isDefer, usesGr bool, fnToCall, errorInfo string) string {
	isHaxeAPI := false
	hashIf := ""  // #if  - only if required
	hashEnd := "" // #end - ditto
	ret := ""

	//special case of: defer close(x)
	if isDefer && isBuiltin && fnToCall == "close" {
		fnToCall = "(new Closure(Go_haxegoruntime_defer_close.call,[]))"
		isBuiltin = false
	}

	if isBuiltin {
		if register != "" {
			register += "="
		}
		switch fnToCall { // TODO handle other built-in functions?
		case "len", "cap":
			switch args[0].Type().Underlying().(type) {
			case *types.Chan, *types.Slice:
				if fnToCall == "len" {
					return register + "({var _v=" + l.IndirectValue(args[0], errorInfo) + ";_v==null?0:(_v.len());});"
				}
				// cap
				return register + "({var _v=" + l.IndirectValue(args[0], errorInfo) + ";_v==null?0:(_v.cap());});"
			case *types.Array: // assume len (same as cap anyway)
				return register + l.IndirectValue(args[0], errorInfo /*, false*/) + ".length;"
			case *types.Map: // assume len(map)
				return register + l.IndirectValue(args[0], errorInfo) + "==null?0:(" +
					l.IndirectValue(args[0], errorInfo) + ".len());"
			case *types.Basic: // assume string as anything else would have produced an error previously
				return register + "Force.toUTF8length(this._goroutine," + l.IndirectValue(args[0], errorInfo /*, false*/) + ");"
			default: // TODO handle other types?
				// TODO error on string?
				pogo.LogError(errorInfo, "Haxe", fmt.Errorf("haxe.Call() - unhandled len/cap type: %s",
					reflect.TypeOf(args[0].Type().Underlying())))
				return register + `null;`
			}
		case "print", "println":
			ret += "Console." + fnToCall + "(["
			/* DEBUG if we want to know where all the prints happen
			ret	+= fmt.Sprintf("Go.CPos(%d)", pogo.LatestValidPosHash)
			if len(args) > 0 {                  // if there are more arguments to pass, add a comma
				ret += ","
			}
			*/
		case "delete":
			return register + l.IndirectValue(args[0], errorInfo) + ".remove(" +
				serializeKey(l.IndirectValue(args[1], errorInfo),
					l.LangType(args[1].Type().Underlying(), false, errorInfo)) + ");"
		case "append":
			return register + l.append(args, errorInfo) + ";"
		case "copy": //TODO rework & test
			return l.copy(register, args, errorInfo) + ";"
		case "close":
			return register + "" + l.IndirectValue(args[0], errorInfo) + ".close();"
		case "recover":
			return register + "" + "Scheduler.recover(this._goroutine);"
		case "real":
			return register + "" + l.IndirectValue(args[0], errorInfo) + ".real;"
		case "imag":
			return register + "" + l.IndirectValue(args[0], errorInfo) + ".imag;"
		case "complex":
			return register + "new Complex(" + l.IndirectValue(args[0], errorInfo) + "," + l.IndirectValue(args[1], errorInfo) + ");"
		case "ssa:wrapnilchk":
			return register + "Scheduler.wrapnilchk(" + l.IndirectValue(args[0], errorInfo) + ");"
		default:
			pogo.LogError(errorInfo, "Haxe", fmt.Errorf("haxe.Call() - Unhandled builtin function: %s", fnToCall))
			ret = "MISSING_BUILTIN("
		}
	} else {
		switch fnToCall {

		//
		// Go library complex function rewriting
		//
		case "runtime_BBreakpoint":
			nextReturnAddress-- //decrement to set new return address for next call generation
			return "this.breakpoint();"
		case "runtime_UUnzipTTestFFSS":
			nextReturnAddress-- //decrement to set new return address for next call generation
			if pogo.LanguageList[langIdx].TestFS != "" {
				return `Go_syscall_UUnzipFFSS.hx("` + pogo.LanguageList[langIdx].TestFS + `");`
			}
			return ""
		//case "math_Inf":
		//	nextReturnAddress-- //decrement to set new return address for next call generation
		//	return register + "=(" + l.IndirectValue(args[0], errorInfo) + ">=0?Math.POSITIVE_INFINITY:Math.NEGATIVE_INFINITY);"

		default:
			//
			// haxe interface pseudo-function re-writing
			//
			if strings.HasPrefix(fnToCall, pseudoFnPrefix) {
				nextReturnAddress-- //decrement to set new return address for next call generation
				if register != "" {
					register += "="
				}
				return register + l.hxPseudoFuncs(fnToCall, args, errorInfo)
			}

			pn := getPackagePath(&cc)
			pnSplit := strings.Split(pn, "/")
			pn = pnSplit[len(pnSplit)-1]
			//fmt.Println("DEBUG package name", pn)

			targetFunc := "Go_" + fnToCall + ".call"

			if strings.HasPrefix(pn, "_") && // in a package that starts with "_"
				!strings.HasPrefix(fnToCall, "_t") { // and not a temp var TODO this may not always be accurate
				//fmt.Println("start _HAXELIB SPECIAL PROCESSING", pn, fnToCall)

				// remove double uppercase characters in name
				ftc := ""
				skip := false
				for _, c := range fnToCall {
					if skip {
						skip = false
					} else {
						ftc += string(c)
						if unicode.IsUpper(c) {
							skip = true
						}
					}
				}
				fnToCall = ftc // fnToCall does not now contain doubled uppercase chars

				nextReturnAddress--                     // decrement to set new return address for next call generation
				isBuiltin = true                        // pretend we are in a builtin function to avoid passing 1st param as bindings
				isHaxeAPI = true                        // we are calling a Haxe native function
				bits := strings.Split(fnToCall, "_47_") // split the parts of the string separated by /
				endbit := bits[len(bits)-1]
				foundDot := false
				if strings.Contains(endbit, "_dot_") { // it's a dot
					ss := strings.Split(endbit, "_dot_")
					endbit = "_ignore_" + ss[len(ss)-1]
					foundDot = true
				}
				bits = strings.Split(endbit, "_") // split RHS after / into parts separated by _
				bits = bits[2:]                   // discard the leading _ and package name
				switch bits[0][0:1] {             // the letter that gives the Haxe language in which to use the api
				case "X": // cross platform, so noOp
				case "P":
					hashIf = " #if cpp "
					hashEnd = " #end "
				case "C":
					hashIf = " #if cs "
					hashEnd = " #end "
				case "F":
					hashIf = " #if flash "
					hashEnd = " #end "
				case "J":
					hashIf = " #if java "
					hashEnd = " #end "
				case "S":
					hashIf = " #if js "
					hashEnd = " #end "
				case "N":
					hashIf = " #if neko "
					hashEnd = " #end "
				case "H":
					hashIf = " #if php "
					hashEnd = " #end "
				case "i":
					if bits[0] == "init" {
						return "" // no calls to _haxelib init functions
					}
					fallthrough
				default:
					pogo.LogError(errorInfo, "Haxe", fmt.Errorf("call to function %s unknown Haxe API first letter %v of %v",
						fnToCall, bits[0][0:1], bits))
				}
				bits[0] = bits[0][1:] // discard the magic letter from the front of the function name

				if foundDot { // it's a Haxe method
					switch bits[len(bits)-1] {
					case "g": // get
						if register != "" {
							ret := l.IndirectValue(args[0], errorInfo) + "." + bits[len(bits)-2][1:]
							r := cc.Signature().Results()
							if r.Len() == 1 {
								switch r.At(0).Type().Underlying().(type) {
								case *types.Interface:
									ret = "Interface.fromDynamic(" + ret + ")"
								case *types.Basic:
									if r.At(0).Type().Underlying().(*types.Basic).Kind() == types.String {
										ret = "Force.fromHaxeString(" + ret + ")"
									}
								}
							}
							return hashIf + register + "=" + ret + ";" + hashEnd
						}
						return ""
					case "s": // set
						interfaceSuffix := ""
						interfacePrefix := ""
						switch args[1].Type().Underlying().(type) {
						case *types.Basic:
							if args[1].Type().Underlying().(*types.Basic).Kind() == types.String {
								interfacePrefix = "Force.toHaxeString("
								interfaceSuffix = ")"
							}
						case *types.Interface:
							interfacePrefix = "Force.toHaxeParam("
							interfaceSuffix = ")"
						}
						return hashIf + "" + l.IndirectValue(args[0], errorInfo) +
							"." + bits[len(bits)-2][1:] +
							"=" + interfacePrefix + l.IndirectValue(args[1], errorInfo) + interfaceSuffix + ";" + hashEnd
					default:
						bits = bits[:len(bits)-1]                                                      //  trim off the "_digit" suffix
						targetFunc = l.IndirectValue(args[0], errorInfo) + "." + bits[len(bits)-1][1:] //remove leading capital letter

						args = args[1:]
					}
				} else {
					switch bits[len(bits)-1] {
					case "g": // special processing to get a class static variable or enum
						if register != "" {
							ret := strings.Join(strings.Split(strings.Join(bits[:len(bits)-1], "."), "..."), "_")
							r := cc.Signature().Results()
							if r.Len() == 1 {
								switch r.At(0).Type().Underlying().(type) {
								case *types.Interface:
									ret = "Interface.fromDynamic(" + ret + ")"
								case *types.Basic:
									if r.At(0).Type().Underlying().(*types.Basic).Kind() == types.String {
										ret = "Force.fromHaxeString(" + ret + ")"
									}
								}
							}
							return hashIf + register + "=" + ret + ";" + hashEnd
						}
						return ""
					case "s": // special processing to set a class static variable
						interfaceSuffix := ""
						interfacePrefix := ""
						switch args[0].Type().Underlying().(type) {
						case *types.Basic:
							if args[0].Type().Underlying().(*types.Basic).Kind() == types.String {
								interfacePrefix = "Force.toHaxeString("
								interfaceSuffix = ")"
							}
						case *types.Interface:
							interfacePrefix = "Force.toHaxeParam("
							interfaceSuffix = ")"
						}
						return hashIf + strings.Join(strings.Split(strings.Join(bits[:len(bits)-1], "."), "..."), "_") +
							"=" + interfacePrefix + l.IndirectValue(args[0], errorInfo) + interfaceSuffix + ";" + hashEnd
					default:
						bits = bits[:len(bits)-1] //  trim off the "_digit" suffix
						if bits[len(bits)-1] == "new" {
							targetFunc = "new " + strings.Join(bits[:len(bits)-1], ".") // put it back into the Haxe format for names
						} else {
							targetFunc = strings.Join(bits, ".") // put it back into the Haxe format for names
						}
					}
				}
				targetFunc = strings.Join(strings.Split(targetFunc, "..."), "_")
				// end _HAXELIB SPECIAL PROCESSING
			} else {
				olv, ok := fnToVarOverloadMap[fnToCall]
				if ok { // replace the function call with a variable
					nextReturnAddress-- //decrement to set new return address for next call generation
					if register == "" {
						return ""
					}
					return register + "=" + olv + ";"
				}
				olf, ok := fnOverloadMap[fnToCall]
				if ok { // replace one go function with another
					targetFunc = olf
				} else {
					olf, ok := builtinOverloadMap[fnToCall]
					if ok { // replace a go function with a haxe one
						targetFunc = olf
						nextReturnAddress-- //decrement to set new return address for next call generation
						isBuiltin = true    // pretend we are in a builtin function to avoid passing 1st param as bindings or waiting for completion
					} else {
						// TODO at this point the package-level overloading could occur, but I cannot make it reliable, so code removed
					}
				}
			}

			switch cc.Value.(type) {
			case *ssa.Function: //simple case
				ret += targetFunc + "("
			case *ssa.MakeClosure: // it is a closure, but with a static callee
				ret += targetFunc + "("
			default: // closure with a dynamic callee
				ret += "Closure.callFn(" + fnToCall + ",[" // the callee is in a local variable
			}
		}
	}
	if !isBuiltin {
		if isGo {
			ret += "Scheduler.makeGoroutine(),"
		} else {
			ret += "this._goroutine,"
		}
	}
	switch cc.Value.(type) {
	case *ssa.Function: //simple case
		if !isBuiltin { // don't pass bindings to built-in functions
			ret += "[]" // goroutine + bindings
		}
	case *ssa.MakeClosure: // it is a closure, but with a static callee
		ret += l.IndirectValue(cc.Value, errorInfo) + "==null?null:" + l.IndirectValue(cc.Value, errorInfo) + ".bds"
	default: // closure with a dynamic callee
		if !isBuiltin { // don't pass bindings to built-in functions
			ret += fnToCall + "==null?null:" + fnToCall + ".bds"
		}
	}
	for arg := range args {
		if arg != 0 || !isBuiltin {
			ret += ","
		}
		// SAME LOGIC AS SWITCH IN INVOKE - keep in line
		switch args[arg].Type().Underlying().(type) { // TODO this may be in need of further optimization
		case *types.Pointer, *types.Slice, *types.Chan: // must pass a reference, not a copy
			ret += l.IndirectValue(args[arg], errorInfo)
		case *types.Interface:
			if isHaxeAPI {
				ret += "Force.toHaxeParam(" + l.IndirectValue(args[arg], errorInfo) + ")"
			} else {
				ret += l.IndirectValue(args[arg], errorInfo)
			}
		case *types.Basic:
			if isHaxeAPI && args[arg].Type().Underlying().(*types.Basic).Kind() == types.String {
				ret += "Force.toHaxeString(" + l.IndirectValue(args[arg], errorInfo) + ")"
			} else {
				ret += l.IndirectValue(args[arg], errorInfo)
			}
		default:
			ret += l.IndirectValue(args[arg], errorInfo)
		}
	}
	if isBuiltin {
		switch fnToCall {
		case "print", "println":
			ret += "]"
		}
		ret += ")"
	} else {
		switch cc.Value.(type) {
		case *ssa.Function, *ssa.MakeClosure: // it is a call with a list of args
			ret += ")"
		default: // it is a call with a single arg that is a list
			ret += "])" // the callee is in a local variable
		}
	}
	if isBuiltin {
		if isGo || isDefer {
			pogo.LogError(errorInfo, "Haxe",
				fmt.Errorf("calling a builtin function (%s) via 'go' or 'defer' is not supported",
					fnToCall))
		}
		if register != "" {
			//**************************
			// ensure correct conversions for interface{} <-> Dynamic when isHaxeAPI
			//**************************
			if isHaxeAPI {
				r := cc.Signature().Results()
				if r.Len() == 1 {
					switch r.At(0).Type().Underlying().(type) {
					case *types.Interface:
						ret = "Interface.fromDynamic(" + ret + ")"
					case *types.Basic:
						if r.At(0).Type().Underlying().(*types.Basic).Kind() == types.String {
							ret = "Force.fromHaxeString(" + ret + ")"
						}
					}
				}
			}
			return hashIf + register + "=" + ret + ";" + hashEnd
		}
		return hashIf + ret + ";" + hashEnd
	}
	if isGo {
		if isDefer {
			pogo.LogError(errorInfo, "Haxe",
				fmt.Errorf("calling a function (%s) using both 'go' and 'defer' is not supported",
					fnToCall))
		}
		return ret + "; "
	}
	if isDefer {
		return ret + ";\nthis.defer(Scheduler.pop(this._goroutine));"
	}
	return l.doCall(register, cc.Signature().Results(), ret+";\n", usesGr)
}

func (l langType) RunDefers(usesGr bool) string {
	return l.doCall("", nil, "this.runDefers();\n", usesGr)
}

func (l langType) doCall(register string, tuple *types.Tuple, callCode string, usesGr bool) string {
	ret := ""
	if register != "" {
		ret += fmt.Sprintf("_SF%d=", -nextReturnAddress)
	}
	if usesGr {
		ret += callCode
		//await completion
		ret += fmt.Sprintf("_Next = %d;\n", nextReturnAddress) // where to come back to
		hadBlockReturn = false
		ret += "return this;\n"
		ret += "#if js } #end\n"
		ret += emitUnseenPseudoBlocks()
		ret += fmt.Sprintf("#if !js case %d: #end\n", nextReturnAddress) // emit code to come back to
		ret += fmt.Sprintf("#if js function _Block_%d(){ #end\n",
			-nextReturnAddress) // optimize JS with closure to allow V8 to optimize big funcs
		if pogo.DebugFlag {
			ret += "this.setLatest(" + fmt.Sprintf("%d", pogo.LatestValidPosHash) + "," + fmt.Sprintf("%d", nextReturnAddress) + ");\n"
		}
		ret += emitTrace(fmt.Sprintf("Block:%d", nextReturnAddress))
	} else {
		callCode = strings.TrimSpace(callCode)
		if register != "" {
			ret += callCode
			ret += emitTrace(`OPTIMIZED CALL (via stack frame)`)
			ret += fmt.Sprintf("_SF%d.run();\n", -nextReturnAddress)
		} else {
			if strings.HasSuffix(callCode, ";") {
				ret += emitTrace(`OPTIMIZED CALL (no stack frame)`)
				ret += fmt.Sprintf("%s.run();\n", strings.TrimSuffix(callCode, ";"))
			} else {
				ret += emitTrace(`OPTIMIZED CALL (via scheduler)`)
				ret += fmt.Sprintf("Scheduler.run1();\n")
				//was: ret += "Scheduler.run1(this._goroutine);\n"
			}
		}
	}
	if register != "" { // if register, set return value, but only for non-null stack frames
		registerZero := ""
		switch tuple.Len() {
		case 0: // nothing to do
		case 1:
			registerZero = l.LangType(tuple.At(0).Type(), true, callCode)
		default:
			registerZero = l.LangType(tuple, true, callCode)
		}
		if registerZero != "" {
			//ret += fmt.Sprintf("%s=(_SF%d==null)?%s:_SF%d.res();\n", // goroutine of -1 => null closure
			//	register, -nextReturnAddress, registerZero, -nextReturnAddress)
			ret += fmt.Sprintf("%s=_SF%d.res();\n", // will fail if _SF is null
				register, -nextReturnAddress)
		}
	}
	nextReturnAddress-- //decrement to set new return address for next call generation
	return ret
}

func allocNewObject(t types.Type) string {
	typ := t.Underlying().(*types.Pointer).Elem().Underlying()
	switch typ.(type) {

	// this should not be required...
	case *types.Array:
		ao := haxeStdSizes.Alignof(typ.(*types.Array).Elem().Underlying())
		so := haxeStdSizes.Sizeof(typ.(*types.Array).Elem().Underlying())
		for so%ao != 0 {
			so++
		}
		return fmt.Sprintf("new Object(%d) /* Array: %s */",
			typ.(*types.Array).Len()*so, typ.String())

	default:
		return fmt.Sprintf("new Object(%d) /* %s */",
			haxeStdSizes.Sizeof(typ),
			typ.String())
	}
}

func (l langType) Alloc(reg string, heap bool, v interface{}, errorInfo string) string {
	if reg == "" {
		return "" // if the register is not used, don't emit the code!
	}
	/*
		typ := v.(types.Type).Underlying().(*types.Pointer).Elem().Underlying()
		//ele := l.LangType(typ, false, errorInfo)
		ptrTyp := "Pointer"
		switch typ.(type) {
		case *types.Array:
			//ele = l.LangType(typ.(*types.Array).Elem().Underlying(), false, errorInfo)
			ptrTyp = "Pointer"
		case *types.Slice:
			//ele = "Slice"
			ptrTyp = "Pointer"
		case *types.Struct:
			//ele = "Dynamic"
			ptrTyp = "Pointer"
		}
		return reg + "=new " + ptrTyp +
			"(" + l.LangType(typ, true, errorInfo) + ");"
	*/
	/*
		switch typ.(type) {
		case *types.Array:
			typ = typ.(*types.Array).Underlying()
		case *types.Struct:
			typ = typ.(*types.Struct).Underlying()
		default:
			pogo.LogError(errorInfo, "Haxe",
				fmt.Errorf("haxe.Alloc() - unhandled type: %v", reflect.TypeOf(typ)))
			return ""
		}
	*/
	if heap {
		return fmt.Sprintf("%s=new Pointer(%s);", reg, allocNewObject(v.(types.Type)))
	}
	//fmt.Println("DEBUG Alloc on Stack", reg, errorInfo)
	reg2 := strings.Replace(strings.Replace(reg, "[", "", 1), "]", "", 1) // just in case we're in a big init() and are using a register array
	return fmt.Sprintf("%s=new Pointer(%s_stackalloc.clear());", reg, reg2)
}

func (l langType) MakeChan(reg string, v interface{}, errorInfo string) string {
	//typeElem := l.LangType(v.(*ssa.MakeChan).Type().Underlying().(*types.Chan).Elem().Underlying(), false, errorInfo)
	size := l.IndirectValue(v.(*ssa.MakeChan).Size, errorInfo)
	return reg + "=new Channel(" + size + `);` // <" + typeElem + ">(" + size + `);`
}

func newSliceCode(typeElem, initElem, capacity, length, errorInfo, itemSize string) string {
	//return "new Slice(new Pointer(new Make<" + typeElem + ">((" + capacity + ")*(" + itemSize + "))" +
	//	".array(" + initElem + "," + capacity + ")" +
	//	"),0," + length + "," + capacity + "," + itemSize + `)`
	return "new Slice(new Pointer(new Object((" + capacity + ")*(" + itemSize + "))" +
		"),0," + length + "," + capacity + "," + itemSize + `)`
}

func (l langType) MakeSlice(reg string, v interface{}, errorInfo string) string {
	typeElem := l.LangType(v.(*ssa.MakeSlice).Type().Underlying().(*types.Slice).Elem().Underlying(), false, errorInfo)
	initElem := l.LangType(v.(*ssa.MakeSlice).Type().Underlying().(*types.Slice).Elem().Underlying(), true, errorInfo)
	length := wrapForce_toUInt(l.IndirectValue(v.(*ssa.MakeSlice).Len, errorInfo),
		v.(*ssa.MakeSlice).Len.Type().Underlying().(*types.Basic).Kind()) // lengths can't be 64 bit
	capacity := wrapForce_toUInt(l.IndirectValue(v.(*ssa.MakeSlice).Cap, errorInfo),
		v.(*ssa.MakeSlice).Cap.Type().Underlying().(*types.Basic).Kind()) // capacities can't be 64 bit
	itemSize := "1" + arrayOffsetCalc(v.(*ssa.MakeSlice).Type().Underlying().(*types.Slice).Elem().Underlying())
	return reg + "=" + newSliceCode(typeElem, initElem, capacity, length, errorInfo, itemSize) + `;`
}

// TODO see http://tip.golang.org/doc/go1.2#three_index
// TODO add third parameter when SSA code provides it to enable slice instructions to specify a capacity
func (l langType) Slice(register string, x, lv, hv interface{}, errorInfo string) string {
	xString := l.IndirectValue(x, errorInfo) // the target must be an array
	if xString == "" {
		xString = l.IndirectValue(x, errorInfo)
	}
	lvString := "0"
	if lv != nil {
		lvString = wrapForce_toUInt(l.IndirectValue(lv, errorInfo),
			lv.(ssa.Value).Type().Underlying().(*types.Basic).Kind())
	}
	hvString := "-1"
	if hv != nil {
		hvString = wrapForce_toUInt(l.IndirectValue(hv, errorInfo),
			hv.(ssa.Value).Type().Underlying().(*types.Basic).Kind())
	}
	switch x.(ssa.Value).Type().Underlying().(type) {
	case *types.Slice:
		return register + "=" + xString + "==null?null:(" + xString + `.subSlice(` + lvString + `,` + hvString + `));`
	case *types.Pointer:
		eleSz := "1" + arrayOffsetCalc(x.(ssa.Value).Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Array).Elem().Underlying())
		return register + "=new Slice(" + xString + `,` + lvString + `,` + hvString + "," +
			fmt.Sprintf("%d", x.(ssa.Value).Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Array).Len()) +
			"," + eleSz + `);`
	case *types.Basic: // assume a string is in need of slicing...
		if hvString == "-1" {
			hvString = "(" + xString + ").length"
		}
		return register + "= (" + xString + ").substr(" + lvString + "," + hvString + "-" + lvString + ") ;"
	default:
		pogo.LogError(errorInfo, "Haxe",
			fmt.Errorf("haxe.Slice() - unhandled type: %v", reflect.TypeOf(x.(ssa.Value).Type().Underlying())))
		return ""
	}
}

func (l langType) Index(register string, v1, v2 interface{}, errorInfo string) string {
	keyString := wrapForce_toUInt(l.IndirectValue(v2, errorInfo),
		v2.(ssa.Value).Type().Underlying().(*types.Basic).Kind())
	typ := v1.(ssa.Value).Type().Underlying().(*types.Array).Elem().Underlying()
	return register + "=" + //l.IndirectValue(v1, errorInfo) + "[" + l.IndirectValue(v2, errorInfo) + "];" + // assign value
		fmt.Sprintf("%s.get%s%s%s)",
			l.IndirectValue(v1, errorInfo),
			loadStoreSuffix(typ, true),
			keyString,
			arrayOffsetCalc(typ)) + ";"
}

//TODO review parameters required
func (l langType) codeField(v interface{}, fNum int, fName, errorInfo string, isFunctionName bool) string {
	//iv := l.IndirectValue(v, errorInfo)
	//r := fmt.Sprintf("%s[%d] /* %s */ ", iv, fNum, fixKeyWds(fName))
	str := v.(ssa.Value).Type().Underlying().(*types.Struct)
	//if pogo.DebugFlag {
	//	r = "{if(" + iv + "==null) { Scheduler.ioor(); null; } else " + r + ";}"
	//}
	//return fmt.Sprintf(" /* %d */ ", fieldOffset(str, fNum)) +
	return fmt.Sprintf("%s.get%s%d)",
		l.IndirectValue(v, errorInfo),
		loadStoreSuffix(str.Field(fNum).Type().Underlying(), true),
		fieldOffset(str, fNum))
}

//TODO review parameters required
func (l langType) Field(register string, v interface{}, fNum int, fName, errorInfo string, isFunctionName bool) string {
	if register != "" {
		return register + "=" + l.codeField(v, fNum, fName, errorInfo, isFunctionName) + ";"
	}
	return ""
}

// TODO error on 64-bit indexes
func (l langType) RangeCheck(x, i interface{}, length int, errorInfo string) string {
	iStr := l.IndirectValue(i, errorInfo)
	if length <= 0 { // length unknown at compile time
		xStr := l.IndirectValue(x, errorInfo)
		tPtr := x.(ssa.Value).Type().Underlying()
		lStr := "" // should give a Haxe compile time error if this is not set below
		//fmt.Println("DEBUG:", l.LangType(x.(ssa.Value).Type().Underlying(), false, errorInfo))
		if l.LangType(tPtr, false, errorInfo) == "Pointer" {
			tPtr = tPtr.(*types.Pointer).Elem().Underlying()
		}
		switch l.LangType(tPtr, false, errorInfo) {
		case "Slice":
			lStr += xStr + "==null?0:" + xStr + ".len()"
		case "Object":
			lStr += fmt.Sprintf("%d", tPtr.(*types.Array).Len())
		}
		ret := fmt.Sprintf("Scheduler.wraprangechk(%s,%s);", iStr, lStr)
		//fmt.Println("DEBUG:",ret)
		return ret
	}
	// length is known at compile time => an array
	return fmt.Sprintf("Scheduler.wraprangechk(%s,%d);", iStr, length)
}

func (l langType) MakeMap(reg string, v interface{}, errorInfo string) string {
	if reg == "" {
		return ""
	}
	return reg + "=" + l.LangType(v.(*ssa.MakeMap).Type().Underlying(), true, errorInfo) + `;`
}

func serializeKey(val, haxeTyp string) string { // can the key be serialized?
	switch haxeTyp {
	case "String", "Int", "Float", "Bool",
		"Pointer", "Object", "GOint64", "Complex", "Interface", "Channel", "Slice":
		return val
	default:
		pogo.LogError("serializeKey", "haxe", errors.New("unsupported map key type: "+haxeTyp))
		return ""
	}
}

func (l langType) MapUpdate(Map, Key, Value interface{}, errorInfo string) string {
	skey := serializeKey(l.IndirectValue(Key, errorInfo),
		l.LangType(Key.(ssa.Value).Type().Underlying(), false, errorInfo))
	ret := l.IndirectValue(Map, errorInfo) + ".set("
	ret += skey + "," //+ l.IndirectValue(Key, errorInfo) + ","
	ret += l.IndirectValue(Value, errorInfo) + ");"
	return ret
}

func (l langType) Lookup(reg string, Map, Key interface{}, commaOk bool, errorInfo string) string {
	if reg == "" {
		return ""
	}
	keyString := l.IndirectValue(Key, errorInfo)
	// check if we are looking up in a string
	if l.LangType(Map.(ssa.Value).Type().Underlying(), false, errorInfo) == "String" {
		keyString = wrapForce_toUInt(keyString, Key.(ssa.Value).Type().Underlying().(*types.Basic).Kind())
		valueCode := l.IndirectValue(Map, errorInfo) //+ ".charCodeAt(" + keyString + ")"
		if commaOk {
			return reg + "=Force.stringAtOK(" + valueCode + "," + keyString + ");"
			//return reg + "=(" + valueCode + "==null) ?" +
			//	"{r0:0,r1:false}:{r0:Std.int(" + valueCode + "),r1:true};"
		}
		return reg + "=Force.stringAt(" + valueCode + "," + keyString + ");"
		//return reg + "=(" + valueCode + "==null) ?" +
		//	"{Scheduler.ioor();0;}:Std.int(" + valueCode + ");"
	}
	// assume it is a Map
	keyString = serializeKey(keyString, l.LangType(Key.(ssa.Value).Type().Underlying(), false, errorInfo))

	isNull := l.IndirectValue(Map, errorInfo) + "==null?"

	li := l.LangType(Map.(ssa.Value).Type().Underlying().(*types.Map).Elem().Underlying(), true, errorInfo)
	if strings.HasPrefix(li, "new ") {
		li = "null" // no need for a full object declaration in this context
	}
	returnValue := l.IndirectValue(Map, errorInfo) + ".get(" + keyString + ")" //.val
	//ltEle := l.LangType(Map.(ssa.Value).Type().Underlying().(*types.Map).Elem().Underlying(), false, errorInfo)
	//switch ltEle {
	//case "GOint64", "Int", "Float", "Bool", "String", "Pointer", "Slice":
	//	returnValue = "cast(" + returnValue + "," + ltEle + ")"
	//}
	eleExists := l.IndirectValue(Map, errorInfo) + ".exists(" + keyString + ")"
	if commaOk {
		return reg + "=" + isNull + "{r0:" + li + ",r1:false}:{r0:" + returnValue + ",r1:" + eleExists + "};"
	}
	return reg + "=" + isNull + li + ":" + returnValue + ";" // the .get will check for existance and return the zero value if not
}

func (l langType) Extract(reg string, tuple interface{}, index int, errorInfo string) string {
	tp := l.IndirectValue(tuple, errorInfo)
	if pogo.DebugFlag {
		tp = "Force.checkTuple(" + tp + ")"
	}
	return reg + "=" + tp + ".r" + fmt.Sprintf("%d", index) + ";"
}

func (l langType) Range(reg string, v interface{}, errorInfo string) string {

	switch l.LangType(v.(ssa.Value).Type().Underlying(), false, errorInfo) {
	case "String":
		return reg + "=new GOstringRange(this._goroutine," + l.IndirectValue(v, errorInfo) + ");"
		//return reg + "={k:0,v:Force.toUTF8slice(this._goroutine," + l.IndirectValue(v, errorInfo) + ")" + "};"
	default: // assume it is a Map {k: key itterator,m: the map,z: zero value of an entry}
		return reg + "=" + l.IndirectValue(v, errorInfo) + "==null?null:cast(" + l.IndirectValue(v, errorInfo) + ",GOmap).range();"
		/*
			keyTyp := l.LangType(v.(ssa.Value).Type().Underlying().(*types.Map).Key().Underlying(), false, errorInfo)
			if keyTyp != "Int" {
				keyTyp = "String"
			}
			return reg + "={k:" + l.IndirectValue(v, errorInfo) + ".keys(),m:" + l.IndirectValue(v, errorInfo) +
				",zk:" + l.LangType(v.(ssa.Value).Type().Underlying().(*types.Map).Key().Underlying(), true, errorInfo) +
				",zv:" + l.LangType(v.(ssa.Value).Type().Underlying().(*types.Map).Elem().Underlying(), true, errorInfo) +

				//`,fk:function(m:` + l.LangType(v.(ssa.Value).Type().Underlying(), false, errorInfo) + ",k:" +
				//keyTyp + "):" +
				//l.LangType(v.(ssa.Value).Type().Underlying().(*types.Map).Key().Underlying(), false, errorInfo) +
				//"{return m.get(" + "k" + ").key;}" +
				//`,fv:function(m:` + l.LangType(v.(ssa.Value).Type().Underlying(), false, errorInfo) + ",k:" +
				//keyTyp + "):" +
				//l.LangType(v.(ssa.Value).Type().Underlying().(*types.Map).Elem().Underlying(), false, errorInfo) +
				//"{return m.get(" + "k" + ").val;}" +

				`};`
		*/
	}
}
func (l langType) Next(register string, v interface{}, isString bool, errorInfo string) string {
	if isString {
		return register + "=cast(" + l.IndirectValue(v, errorInfo) + ",GOstringRange).next();"
		/*
			return register + "={var _thisK:Int=" + l.IndirectValue(v, errorInfo) + ".k;" +
				"if(" + l.IndirectValue(v, errorInfo) + ".k>=" + l.IndirectValue(v, errorInfo) + ".v.len()){r0:false,r1:0,r2:0};" +
				"else {" +
				"var _dr:{r0:Int,r1:Int}=Go_utf8_DDecodeRRune.callFromRT(this._goroutine," + l.IndirectValue(v, errorInfo) +
				".v.subSlice(_thisK,-1));" +
				l.IndirectValue(v, errorInfo) + ".k+=_dr.r1;" +
				"{r0:true,r1:cast(_thisK,Int),r2:cast(_dr.r0,Int)};}};"
		*/
	}
	// otherwise it is a map itterator
	return register + "=" + l.IndirectValue(v, errorInfo) + "==null?{r0:false,r1:null,r2:null}:cast(" + l.IndirectValue(v, errorInfo) + ",GOmapRange).next();"
	/*
		return register + "={var _hn:Bool=" + l.IndirectValue(v, errorInfo) + ".k.hasNext();\n" +
			"if(_hn){var _nxt=" + l.IndirectValue(v, errorInfo) + ".k.next();\n" +
			//"$type(" + l.IndirectValue(v, errorInfo) + ".m);\n" +
			"{r0:true,r1:" + l.IndirectValue(v, errorInfo) + ".m.get(_nxt).key," +
			"r2:" + l.IndirectValue(v, errorInfo) + ".m.get(_nxt).val};\n" +
			"}else{{r0:false,r1:" + l.IndirectValue(v, errorInfo) + ".zk,r2:" + l.IndirectValue(v, errorInfo) + ".zv};\n}};"
	*/
}

func (l langType) MakeClosure(reg string, v interface{}, errorInfo string) string {
	// use a closure type
	ret := reg + "= new Closure(" + l.IndirectValue(v.(*ssa.MakeClosure).Fn, errorInfo) + ",{"
	for b := range v.(*ssa.MakeClosure).Bindings {
		if b != 0 {
			ret += ","
		}
		ret += `"` + v.(*ssa.MakeClosure).Fn.(*ssa.Function).FreeVars[b].Name() + `": `
		ret += l.IndirectValue(v.(*ssa.MakeClosure).Bindings[b], errorInfo)
	}
	return ret + "});"

	//it does not work to try just returning the function, and let the invloking call do the binding
	//as in: return reg + "=" + l.IndirectValue(v.(*ssa.MakeClosure).Fn, errorInfo) + ";"
}

func (l langType) EmitInvoke(register string, isGo, isDefer, usesGr bool, callCommon interface{}, errorInfo string) string {
	val := callCommon.(ssa.CallCommon).Value
	meth := callCommon.(ssa.CallCommon).Method.Name()
	ret := ""
	if pogo.DebugFlag {
		ret += l.IndirectValue(val, errorInfo) + "==null?Scheduler.unt():"
	}
	ret += "Interface.invoke(" + l.IndirectValue(val, errorInfo) + `,"` + meth + `",[`
	if isGo {
		if isDefer {
			pogo.LogError(errorInfo, "Haxe",
				fmt.Errorf("calling a method (%s) using both 'go' and 'defer' is not supported",
					meth))
		}
		ret += "Scheduler.makeGoroutine()"
	} else {
		ret += "this._goroutine"
	}
	ret += `,[],` + l.IndirectValue(val, errorInfo) + ".val"
	args := callCommon.(ssa.CallCommon).Args
	for arg := range args {
		ret += ","
		// SAME LOGIC AS SWITCH IN CALL - keep in line
		switch args[arg].Type().Underlying().(type) { // TODO this may be in need of further optimization
		case *types.Pointer, *types.Slice, *types.Chan: // must pass a reference, not a copy
			ret += l.IndirectValue(args[arg], errorInfo)
		case *types.Basic, *types.Interface: // NOTE Complex is an object as is Int64 (in java & cs), but copy does not seem to be required
			ret += l.IndirectValue(args[arg], errorInfo)
		default: // TODO review
			ret += l.IndirectValue(args[arg], errorInfo)
		}
	}
	if isGo {
		return ret + "]); "
	}
	if isDefer {
		return ret + "]);\nthis.defer(Scheduler.pop(this._goroutine));"
	}
	cc := callCommon.(ssa.CallCommon)
	return l.doCall(register, cc.Signature().Results(), ret+"]);", usesGr)
}

func (l langType) SubFnStart(id int, mustSplitCode bool) string {
	if !mustSplitCode {
		return "try {"
	}
	return fmt.Sprintf("private "+"function SubFn%d():Void { try {", id)
}

func (l langType) SubFnEnd(id, pos int, mustSplitCode bool) string {
	ret := fmt.Sprintf("} catch (c:Dynamic) {Scheduler.htc(c,%d);}", pos)
	if mustSplitCode {
		ret += ";}"
	}
	return ret
}

func (l langType) SubFnCall(id int) string {
	return fmt.Sprintf("SubFn%d();", id)
}

func (l langType) DeclareTempVar(v ssa.Value) string {
	if useRegisterArray {
		return ""
	}
	typ := l.LangType(v.Type(), false, "temp var declaration")
	if typ == "" {
		return ""
	}
	// NOTE testing has demonstrated that JS temp var init improves V8 optimization & so speeds-up subFns
	init := l.LangType(v.Type(), true, "temp var declaration")
	if strings.HasPrefix(init, "new") || strings.HasPrefix(init, "{") || strings.HasPrefix(init, "GOint64") {
		init = "null"
	}
	if init == "null" {
		init = "" // in JS null is the default
	} else {
		init = "#if js =" + init + " #end " // to allow V8 optimisation
	}
	return "var _" + v.Name() + ":" + typ + " " + init + ";"
}

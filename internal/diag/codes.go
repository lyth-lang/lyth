package diag

// Diagnostic codes. Codes are stable: once released, a code never changes
// meaning. The catalog is documented in docs/design/diagnostics.md.
const (
	// Syntax.
	EInvalidChar       Code = "E0001"
	EUnterminated      Code = "E0002"
	EUnexpectedToken   Code = "E0003"
	EInvalidNumber     Code = "E0004"
	EColonEquals       Code = "E0005"
	EDollarTemplate    Code = "E0006"
	EInvalidEscape     Code = "E0007"
	EUnsupportedSyntax Code = "E0008"

	// Names and packages.
	EUndefined      Code = "E0101"
	EDuplicate      Code = "E0102"
	EImport         Code = "E0103"
	ENotExpected    Code = "E0104"
	EPrivate        Code = "E0105"
	EUnknownPkgName Code = "E0106"

	// Types.
	ETypeMismatch    Code = "E0201"
	EOperandMismatch Code = "E0202"
	EArgCount        Code = "E0203"
	ENotCallable     Code = "E0204"
	EUnknownMember   Code = "E0205"
	EMissingReturn   Code = "E0206"
	EReturnMismatch  Code = "E0207"
	ECannotInfer     Code = "E0208"
	EInvalidOp       Code = "E0209"
	EMultiValue      Code = "E0210"
	EConstruction    Code = "E0211"
	ENotImplement    Code = "E0212"
	ETypeArgs        Code = "E0213"
	EConversion      Code = "E0214"
	EOverflow        Code = "E0215"
	EInvalidUse      Code = "E0216"

	// Errors.
	ETryOutside     Code = "E0301"
	ETryNotFallible Code = "E0302"
	EUnhandledError Code = "E0303"
	EThrowOutside   Code = "E0304"
	EThrowOperand   Code = "E0305"
	ECatchNotFall   Code = "E0306"
	ECatchBlock     Code = "E0307"

	// Nullability.
	ENullable       Code = "E0401"
	ENullAssign     Code = "E0402"
	ENoZeroValue    Code = "E0405"
	ENullableColl   Code = "E0406"
	ENullableTParam Code = "E0407"

	// Mutability.
	EReassignVal  Code = "E0501"
	EMutateVal    Code = "E0502"
	EAddressOfVal Code = "E0503"
	EPtrMethodVal Code = "E0504"

	// Interop.
	EUnsupported Code = "E0601"
	EToolchain   Code = "E0602"

	// Internal and toolchain.
	EInternal    Code = "E0901"
	EGoTypeCheck Code = "E0902"
	EGoCompile   Code = "E0903"
	EGoLink      Code = "E0904"

	// Warnings.
	WUnusedVar     Code = "W0001"
	WUnusedImport  Code = "W0002"
	WPreferThrows  Code = "W0003"
	WUnnecessary   Code = "W0004"
	WUnreachable   Code = "W0005"
	WShadow        Code = "W0006"
	WErrorTypeTest Code = "W0007"
	WJSONKey       Code = "W0008"
	WJSONKeyChange Code = "W0009"
)

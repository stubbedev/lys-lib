package harfbuzz

import (
	"fmt"

	"github.com/stubbedev/lys-lib/third_party/typesetting/font"
	ot "github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype"
	"github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype/tables"
)

// ported from harfbuzz/src/hb-aat-layout.h  Copyright © 2018 Ebrahim Byagowi, Behdad Esfahbod

// The possible feature types defined for AAT shaping,
// from https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html
type aatLayoutFeatureType = uint16

const (
	// Initial, unset feature type
	// aatLayoutFeatureTypeInvalid = 0xFFFF
	// [All Typographic Features](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type0)
	// aatLayoutFeatureTypeAllTypographic = 0
	// [Ligatures](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type1)
	aatLayoutFeatureTypeLigatures = 1
	// [Cursive Connection](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type2)
	// aatLayoutFeatureTypeCurisveConnection = 2
	// [Letter Case](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type3)
	aatLayoutFeatureTypeLetterCase = 3
	// [Vertical Substitution](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type4)
	aatLayoutFeatureTypeVerticalSubstitution = 4
	// [Linguistic Rearrangement](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type5)
	// aatLayoutFeatureTypeLinguisticRearrangement = 5
	// [Number Spacing](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type6)
	aatLayoutFeatureTypeNumberSpacing = 6
	// [Smart Swash](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type8)
	// aatLayoutFeatureTypeSmartSwashType = 8
	// [Diacritics](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type9)
	// aatLayoutFeatureTypeDiacriticsType = 9
	// [Vertical Position](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type10)
	aatLayoutFeatureTypeVerticalPosition = 10
	// [Fractions](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type11)
	aatLayoutFeatureTypeFractions = 11
	// [Overlapping Characters](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type13)
	// aatLayoutFeatureTypeOverlappingCharactersType = 13
	// [Typographic Extras](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type14)
	aatLayoutFeatureTypeTypographicExtras = 14
	// [Mathematical Extras](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type15)
	aatLayoutFeatureTypeMathematicalExtras = 15
	// [Ornament Sets](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type16)
	// aatLayoutFeatureTypeOrnamentSetsType = 16
	// [Character Alternatives](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type17)
	aatLayoutFeatureTypeCharacterAlternatives = 17
	// [Style Options](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type19)
	aatLayoutFeatureTypeStyleOptions = 19
	// [Character Shape](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type20)
	aatLayoutFeatureTypeCharacterShape = 20
	// [Number Case](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type21)
	aatLayoutFeatureTypeNumberCase = 21
	// [Text Spacing](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type22)
	aatLayoutFeatureTypeTextSpacing = 22
	// [Transliteration](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type23)
	aatLayoutFeatureTypeTransliteration = 23

	// [Ruby Kana](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type28)
	aatLayoutFeatureTypeRubyKana = 28

	// [Italic CJK Roman](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type32)
	aatLayoutFeatureTypeItalicCjkRoman = 32
	// [Case Sensitive Layout](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type33)
	aatLayoutFeatureTypeCaseSensitiveLayout = 33
	// [Alternate Kana](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type34)
	aatLayoutFeatureTypeAlternateKana = 34
	// [Stylistic Alternatives](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type35)
	aatLayoutFeatureTypeStylisticAlternatives = 35
	// [Contextual Alternatives](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type36)
	aatLayoutFeatureTypeContextualAlternatives = 36
	// [Lower Case](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type37)
	aatLayoutFeatureTypeLowerCase = 37
	// [Upper Case](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type38)
	aatLayoutFeatureTypeUpperCase = 38
	// [Language Tag](https://developer.apple.com/fonts/TrueType-Reference-Manual/RM09/AppendixF.html#Type39)
	aatLayoutFeatureTypeLanguageTagType = 39
)

// The selectors defined for specifying AAT feature settings.
type aatLayoutFeatureSelector = uint16

const (
	/* Selectors for #aatLayoutFeatureTypeLigatures */
	// for #aatLayoutFeatureTypeLigatures
	aatLayoutFeatureSelectorRequiredLigaturesOn = 0
	// for #aatLayoutFeatureTypeLigatures
	aatLayoutFeatureSelectorRequiredLigaturesOff = 1
	// for #aatLayoutFeatureTypeLigatures
	aatLayoutFeatureSelectorCommonLigaturesOn = 2
	// for #aatLayoutFeatureTypeLigatures
	aatLayoutFeatureSelectorCommonLigaturesOff = 3
	// for #aatLayoutFeatureTypeLigatures
	aatLayoutFeatureSelectorRareLigaturesOn = 4
	// for #aatLayoutFeatureTypeLigatures
	aatLayoutFeatureSelectorRareLigaturesOff = 5

	// for #aatLayoutFeatureTypeLigatures
	aatLayoutFeatureSelectorContextualLigaturesOn = 18
	// for #aatLayoutFeatureTypeLigatures
	aatLayoutFeatureSelectorContextualLigaturesOff = 19
	// for #aatLayoutFeatureTypeLigatures
	aatLayoutFeatureSelectorHistoricalLigaturesOn = 20
	// for #aatLayoutFeatureTypeLigatures
	aatLayoutFeatureSelectorHistoricalLigaturesOff = 21

	/* Selectors for #aatLayoutFeatureTypeLetterCase */

	// Deprecated
	aatLayoutFeatureSelectorSmallCaps = 3 /* deprecated */

	/* Selectors for #aatLayoutFeatureTypeVerticalSubstitution */
	// for #aatLayoutFeatureTypeVerticalSubstitution
	aatLayoutFeatureSelectorSubstituteVerticalFormsOn = 0
	// for #aatLayoutFeatureTypeVerticalSubstitution
	aatLayoutFeatureSelectorSubstituteVerticalFormsOff = 1

	/* Selectors for #aatLayoutFeatureTypeNumberSpacing */
	// for #aatLayoutFeatureTypeNumberSpacing
	aatLayoutFeatureSelectorMonospacedNumbers = 0
	// for #aatLayoutFeatureTypeNumberSpacing
	aatLayoutFeatureSelectorProportionalNumbers = 1

	/* Selectors for #aatLayoutFeatureTypeVerticalPosition */
	// for #aatLayoutFeatureTypeVerticalPosition
	aatLayoutFeatureSelectorNormalPosition = 0
	// for #aatLayoutFeatureTypeVerticalPosition
	aatLayoutFeatureSelectorSuperiors = 1
	// for #aatLayoutFeatureTypeVerticalPosition
	aatLayoutFeatureSelectorInferiors = 2
	// for #aatLayoutFeatureTypeVerticalPosition
	aatLayoutFeatureSelectorOrdinals = 3
	// for #aatLayoutFeatureTypeVerticalPosition
	aatLayoutFeatureSelectorScientificInferiors = 4

	/* Selectors for #aatLayoutFeatureTypeFractions */
	// for #aatLayoutFeatureTypeFractions
	aatLayoutFeatureSelectorNoFractions = 0
	// for #aatLayoutFeatureTypeFractions
	aatLayoutFeatureSelectorVerticalFractions = 1
	// for #aatLayoutFeatureTypeFractions
	aatLayoutFeatureSelectorDiagonalFractions = 2

	// for #aatLayoutFeatureTypeTypographicExtras
	aatLayoutFeatureSelectorSlashedZeroOn = 4
	// for #aatLayoutFeatureTypeTypographicExtras
	aatLayoutFeatureSelectorSlashedZeroOff = 5

	/* Selectors for #aatLayoutFeatureTypeMathematicalExtras */
	// for #aatLayoutFeatureTypeMathematicalExtras
	aatLayoutFeatureSelectorMathematicalGreekOn = 10
	// for #aatLayoutFeatureTypeMathematicalExtras
	aatLayoutFeatureSelectorMathematicalGreekOff = 11

	/* Selectors for #aatLayoutFeatureTypeStyleOptions */
	// for #aatLayoutFeatureTypeStyleOptions
	aatLayoutFeatureSelectorNoStyleOptions = 0

	// for #aatLayoutFeatureTypeStyleOptions
	aatLayoutFeatureSelectorTitlingCaps = 4

	/* Selectors for #aatLayoutFeatureTypeCharacterShape */
	// for #aatLayoutFeatureTypeCharacterShape
	aatLayoutFeatureSelectorTraditionalCharacters = 0
	// for #aatLayoutFeatureTypeCharacterShape
	aatLayoutFeatureSelectorSimplifiedCharacters = 1
	// for #aatLayoutFeatureTypeCharacterShape
	aatLayoutFeatureSelectorJis1978Characters = 2
	// for #aatLayoutFeatureTypeCharacterShape
	aatLayoutFeatureSelectorJis1983Characters = 3
	// for #aatLayoutFeatureTypeCharacterShape
	aatLayoutFeatureSelectorJis1990Characters = 4

	// for #aatLayoutFeatureTypeCharacterShape
	aatLayoutFeatureSelectorExpertCharacters = 10
	// for #aatLayoutFeatureTypeCharacterShape
	aatLayoutFeatureSelectorJis2004Characters = 11
	// for #aatLayoutFeatureTypeCharacterShape
	aatLayoutFeatureSelectorHojoCharacters = 12
	// for #aatLayoutFeatureTypeCharacterShape
	aatLayoutFeatureSelectorNlccharacters = 13
	// for #aatLayoutFeatureTypeCharacterShape
	aatLayoutFeatureSelectorTraditionalNamesCharacters = 14

	/* Selectors for #aatLayoutFeatureTypeNumberCase */
	// for #aatLayoutFeatureTypeNumberCase
	aatLayoutFeatureSelectorLowerCaseNumbers = 0
	// for #aatLayoutFeatureTypeNumberCase
	aatLayoutFeatureSelectorUpperCaseNumbers = 1

	/* Selectors for #aatLayoutFeatureTypeTextSpacing */
	// for #aatLayoutFeatureTypeTextSpacing
	aatLayoutFeatureSelectorProportionalText = 0
	// for #aatLayoutFeatureTypeTextSpacing
	aatLayoutFeatureSelectorMonospacedText = 1
	// for #aatLayoutFeatureTypeTextSpacing
	aatLayoutFeatureSelectorHalfWidthText = 2
	// for #aatLayoutFeatureTypeTextSpacing
	aatLayoutFeatureSelectorThirdWidthText = 3
	// for #aatLayoutFeatureTypeTextSpacing
	aatLayoutFeatureSelectorQuarterWidthText = 4
	// for #aatLayoutFeatureTypeTextSpacing
	aatLayoutFeatureSelectorAltProportionalText = 5
	// for #aatLayoutFeatureTypeTextSpacing
	aatLayoutFeatureSelectorAltHalfWidthText = 6

	/* Selectors for #aatLayoutFeatureTypeTransliteration */
	// for #aatLayoutFeatureTypeTransliteration
	aatLayoutFeatureSelectorNoTransliteration = 0
	// for #aatLayoutFeatureTypeTransliteration
	aatLayoutFeatureSelectorHanjaToHangul = 1

	/* Selectors for #aatLayoutFeatureTypeRubyKana */
	// for #aatLayoutFeatureTypeRubyKana
	aatLayoutFeatureSelectorRubyKanaOn = 2
	// for #aatLayoutFeatureTypeRubyKana
	aatLayoutFeatureSelectorRubyKanaOff = 3

	/* Selectors for #aatLayoutFeatureTypeItalicCjkRoman */
	// for #aatLayoutFeatureTypeItalicCjkRoman
	aatLayoutFeatureSelectorCjkItalicRomanOn = 2
	// for #aatLayoutFeatureTypeItalicCjkRoman
	aatLayoutFeatureSelectorCjkItalicRomanOff = 3

	/* Selectors for #aatLayoutFeatureTypeCaseSensitiveLayout */
	// for #aatLayoutFeatureTypeCaseSensitiveLayout
	aatLayoutFeatureSelectorCaseSensitiveLayoutOn = 0
	// for #aatLayoutFeatureTypeCaseSensitiveLayout
	aatLayoutFeatureSelectorCaseSensitiveLayoutOff = 1
	// for #aatLayoutFeatureTypeCaseSensitiveLayout
	aatLayoutFeatureSelectorCaseSensitiveSpacingOn = 2
	// for #aatLayoutFeatureTypeCaseSensitiveLayout
	aatLayoutFeatureSelectorCaseSensitiveSpacingOff = 3

	/* Selectors for #aatLayoutFeatureTypeAlternateKana */
	// for #aatLayoutFeatureTypeAlternateKana
	aatLayoutFeatureSelectorAlternateHorizKanaOn = 0
	// for #aatLayoutFeatureTypeAlternateKana
	aatLayoutFeatureSelectorAlternateHorizKanaOff = 1
	// for #aatLayoutFeatureTypeAlternateKana
	aatLayoutFeatureSelectorAlternateVertKanaOn = 2
	// for #aatLayoutFeatureTypeAlternateKana
	aatLayoutFeatureSelectorAlternateVertKanaOff = 3

	/* Selectors for #aatLayoutFeatureTypeStylisticAlternatives */

	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltOneOn = 2
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltOneOff = 3
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltTwoOn = 4
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltTwoOff = 5
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltThreeOn = 6
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltThreeOff = 7
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltFourOn = 8
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltFourOff = 9
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltFiveOn = 10
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltFiveOff = 11
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltSixOn = 12
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltSixOff = 13
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltSevenOn = 14
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltSevenOff = 15
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltEightOn = 16
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltEightOff = 17
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltNineOn = 18
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltNineOff = 19
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltTenOn = 20
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltTenOff = 21
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltElevenOn = 22
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltElevenOff = 23
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltTwelveOn = 24
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltTwelveOff = 25
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltThirteenOn = 26
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltThirteenOff = 27
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltFourteenOn = 28
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltFourteenOff = 29
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltFifteenOn = 30
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltFifteenOff = 31
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltSixteenOn = 32
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltSixteenOff = 33
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltSeventeenOn = 34
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltSeventeenOff = 35
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltEighteenOn = 36
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltEighteenOff = 37
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltNineteenOn = 38
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltNineteenOff = 39
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltTwentyOn = 40
	// for #aatLayoutFeatureTypeStylisticAlternatives
	aatLayoutFeatureSelectorStylisticAltTwentyOff = 41

	/* Selectors for #aatLayoutFeatureTypeContextualAlternatives */
	// for #aatLayoutFeatureTypeContextualAlternatives
	aatLayoutFeatureSelectorContextualAlternatesOn = 0
	// for #aatLayoutFeatureTypeContextualAlternatives
	aatLayoutFeatureSelectorContextualAlternatesOff = 1
	// for #aatLayoutFeatureTypeContextualAlternatives
	aatLayoutFeatureSelectorSwashAlternatesOn = 2
	// for #aatLayoutFeatureTypeContextualAlternatives
	aatLayoutFeatureSelectorSwashAlternatesOff = 3
	// for #aatLayoutFeatureTypeContextualAlternatives
	aatLayoutFeatureSelectorContextualSwashAlternatesOn = 4
	// for #aatLayoutFeatureTypeContextualAlternatives
	aatLayoutFeatureSelectorContextualSwashAlternatesOff = 5

	/* Selectors for #aatLayoutFeatureTypeLowerCase */
	// for #aatLayoutFeatureTypeLowerCase
	aatLayoutFeatureSelectorDefaultLowerCase = 0
	// for #aatLayoutFeatureTypeLowerCase
	aatLayoutFeatureSelectorLowerCaseSmallCaps = 1
	// for #aatLayoutFeatureTypeLowerCase
	aatLayoutFeatureSelectorLowerCasePetiteCaps = 2

	/* Selectors for #aatLayoutFeatureTypeUpperCase */
	// for #aatLayoutFeatureTypeUpperCase
	aatLayoutFeatureSelectorDefaultUpperCase = 0
	// for #aatLayoutFeatureTypeUpperCase
	aatLayoutFeatureSelectorUpperCaseSmallCaps = 1
	// for #aatLayoutFeatureTypeUpperCase
	aatLayoutFeatureSelectorUpperCasePetiteCaps = 2
)

// Mapping from OpenType feature tags to AAT feature names and selectors.
//
// Table data courtesy of Apple.  Converted from mnemonics to integers
// when moving to this file.
var featureMappings = [...]aatFeatureMapping{
	{ot.Tag('a'<<24 | 'f'<<16 | 'r'<<8 | 'c'), aatLayoutFeatureTypeFractions, aatLayoutFeatureSelectorVerticalFractions, aatLayoutFeatureSelectorNoFractions},
	{ot.Tag('c'<<24 | '2'<<16 | 'p'<<8 | 'c'), aatLayoutFeatureTypeUpperCase, aatLayoutFeatureSelectorUpperCasePetiteCaps, aatLayoutFeatureSelectorDefaultUpperCase},
	{ot.Tag('c'<<24 | '2'<<16 | 's'<<8 | 'c'), aatLayoutFeatureTypeUpperCase, aatLayoutFeatureSelectorUpperCaseSmallCaps, aatLayoutFeatureSelectorDefaultUpperCase},
	{ot.Tag('c'<<24 | 'a'<<16 | 'l'<<8 | 't'), aatLayoutFeatureTypeContextualAlternatives, aatLayoutFeatureSelectorContextualAlternatesOn, aatLayoutFeatureSelectorContextualAlternatesOff},
	{ot.Tag('c'<<24 | 'a'<<16 | 's'<<8 | 'e'), aatLayoutFeatureTypeCaseSensitiveLayout, aatLayoutFeatureSelectorCaseSensitiveLayoutOn, aatLayoutFeatureSelectorCaseSensitiveLayoutOff},
	{ot.Tag('c'<<24 | 'l'<<16 | 'i'<<8 | 'g'), aatLayoutFeatureTypeLigatures, aatLayoutFeatureSelectorContextualLigaturesOn, aatLayoutFeatureSelectorContextualLigaturesOff},
	{ot.Tag('c'<<24 | 'p'<<16 | 's'<<8 | 'p'), aatLayoutFeatureTypeCaseSensitiveLayout, aatLayoutFeatureSelectorCaseSensitiveSpacingOn, aatLayoutFeatureSelectorCaseSensitiveSpacingOff},
	{ot.Tag('c'<<24 | 's'<<16 | 'w'<<8 | 'h'), aatLayoutFeatureTypeContextualAlternatives, aatLayoutFeatureSelectorContextualSwashAlternatesOn, aatLayoutFeatureSelectorContextualSwashAlternatesOff},
	{ot.Tag('d'<<24 | 'l'<<16 | 'i'<<8 | 'g'), aatLayoutFeatureTypeLigatures, aatLayoutFeatureSelectorRareLigaturesOn, aatLayoutFeatureSelectorRareLigaturesOff},
	{ot.Tag('e'<<24 | 'x'<<16 | 'p'<<8 | 't'), aatLayoutFeatureTypeCharacterShape, aatLayoutFeatureSelectorExpertCharacters, 16},
	{ot.Tag('f'<<24 | 'r'<<16 | 'a'<<8 | 'c'), aatLayoutFeatureTypeFractions, aatLayoutFeatureSelectorDiagonalFractions, aatLayoutFeatureSelectorNoFractions},
	{ot.Tag('f'<<24 | 'w'<<16 | 'i'<<8 | 'd'), aatLayoutFeatureTypeTextSpacing, aatLayoutFeatureSelectorMonospacedText, 7},
	{ot.Tag('h'<<24 | 'a'<<16 | 'l'<<8 | 't'), aatLayoutFeatureTypeTextSpacing, aatLayoutFeatureSelectorAltHalfWidthText, 7},
	{ot.Tag('h'<<24 | 'i'<<16 | 's'<<8 | 't'), 40, 0, 1},
	{ot.Tag('h'<<24 | 'k'<<16 | 'n'<<8 | 'a'), aatLayoutFeatureTypeAlternateKana, aatLayoutFeatureSelectorAlternateHorizKanaOn, aatLayoutFeatureSelectorAlternateHorizKanaOff},
	{ot.Tag('h'<<24 | 'l'<<16 | 'i'<<8 | 'g'), aatLayoutFeatureTypeLigatures, aatLayoutFeatureSelectorHistoricalLigaturesOn, aatLayoutFeatureSelectorHistoricalLigaturesOff},
	{ot.Tag('h'<<24 | 'n'<<16 | 'g'<<8 | 'l'), aatLayoutFeatureTypeTransliteration, aatLayoutFeatureSelectorHanjaToHangul, aatLayoutFeatureSelectorNoTransliteration},
	{ot.Tag('h'<<24 | 'o'<<16 | 'j'<<8 | 'o'), aatLayoutFeatureTypeCharacterShape, aatLayoutFeatureSelectorHojoCharacters, 16},
	{ot.Tag('h'<<24 | 'w'<<16 | 'i'<<8 | 'd'), aatLayoutFeatureTypeTextSpacing, aatLayoutFeatureSelectorHalfWidthText, 7},
	{ot.Tag('i'<<24 | 't'<<16 | 'a'<<8 | 'l'), aatLayoutFeatureTypeItalicCjkRoman, aatLayoutFeatureSelectorCjkItalicRomanOn, aatLayoutFeatureSelectorCjkItalicRomanOff},
	{ot.Tag('j'<<24 | 'p'<<16 | '0'<<8 | '4'), aatLayoutFeatureTypeCharacterShape, aatLayoutFeatureSelectorJis2004Characters, 16},
	{ot.Tag('j'<<24 | 'p'<<16 | '7'<<8 | '8'), aatLayoutFeatureTypeCharacterShape, aatLayoutFeatureSelectorJis1978Characters, 16},
	{ot.Tag('j'<<24 | 'p'<<16 | '8'<<8 | '3'), aatLayoutFeatureTypeCharacterShape, aatLayoutFeatureSelectorJis1983Characters, 16},
	{ot.Tag('j'<<24 | 'p'<<16 | '9'<<8 | '0'), aatLayoutFeatureTypeCharacterShape, aatLayoutFeatureSelectorJis1990Characters, 16},
	{ot.Tag('l'<<24 | 'i'<<16 | 'g'<<8 | 'a'), aatLayoutFeatureTypeLigatures, aatLayoutFeatureSelectorCommonLigaturesOn, aatLayoutFeatureSelectorCommonLigaturesOff},
	{ot.Tag('l'<<24 | 'n'<<16 | 'u'<<8 | 'm'), aatLayoutFeatureTypeNumberCase, aatLayoutFeatureSelectorUpperCaseNumbers, 2},
	{ot.Tag('m'<<24 | 'g'<<16 | 'r'<<8 | 'k'), aatLayoutFeatureTypeMathematicalExtras, aatLayoutFeatureSelectorMathematicalGreekOn, aatLayoutFeatureSelectorMathematicalGreekOff},
	{ot.Tag('n'<<24 | 'l'<<16 | 'c'<<8 | 'k'), aatLayoutFeatureTypeCharacterShape, aatLayoutFeatureSelectorNlccharacters, 16},
	{ot.Tag('o'<<24 | 'n'<<16 | 'u'<<8 | 'm'), aatLayoutFeatureTypeNumberCase, aatLayoutFeatureSelectorLowerCaseNumbers, 2},
	{ot.Tag('o'<<24 | 'r'<<16 | 'd'<<8 | 'n'), aatLayoutFeatureTypeVerticalPosition, aatLayoutFeatureSelectorOrdinals, aatLayoutFeatureSelectorNormalPosition},
	{ot.Tag('p'<<24 | 'a'<<16 | 'l'<<8 | 't'), aatLayoutFeatureTypeTextSpacing, aatLayoutFeatureSelectorAltProportionalText, 7},
	{ot.Tag('p'<<24 | 'c'<<16 | 'a'<<8 | 'p'), aatLayoutFeatureTypeLowerCase, aatLayoutFeatureSelectorLowerCasePetiteCaps, aatLayoutFeatureSelectorDefaultLowerCase},
	{ot.Tag('p'<<24 | 'k'<<16 | 'n'<<8 | 'a'), aatLayoutFeatureTypeTextSpacing, aatLayoutFeatureSelectorProportionalText, 7},
	{ot.Tag('p'<<24 | 'n'<<16 | 'u'<<8 | 'm'), aatLayoutFeatureTypeNumberSpacing, aatLayoutFeatureSelectorProportionalNumbers, 4},
	{ot.Tag('p'<<24 | 'w'<<16 | 'i'<<8 | 'd'), aatLayoutFeatureTypeTextSpacing, aatLayoutFeatureSelectorProportionalText, 7},
	{ot.Tag('q'<<24 | 'w'<<16 | 'i'<<8 | 'd'), aatLayoutFeatureTypeTextSpacing, aatLayoutFeatureSelectorQuarterWidthText, 7},
	{ot.Tag('r'<<24 | 'l'<<16 | 'i'<<8 | 'g'), aatLayoutFeatureTypeLigatures, aatLayoutFeatureSelectorRequiredLigaturesOn, aatLayoutFeatureSelectorRequiredLigaturesOff},
	{ot.Tag('r'<<24 | 'u'<<16 | 'b'<<8 | 'y'), aatLayoutFeatureTypeRubyKana, aatLayoutFeatureSelectorRubyKanaOn, aatLayoutFeatureSelectorRubyKanaOff},
	{ot.Tag('s'<<24 | 'i'<<16 | 'n'<<8 | 'f'), aatLayoutFeatureTypeVerticalPosition, aatLayoutFeatureSelectorScientificInferiors, aatLayoutFeatureSelectorNormalPosition},
	{ot.Tag('s'<<24 | 'm'<<16 | 'c'<<8 | 'p'), aatLayoutFeatureTypeLowerCase, aatLayoutFeatureSelectorLowerCaseSmallCaps, aatLayoutFeatureSelectorDefaultLowerCase},
	{ot.Tag('s'<<24 | 'm'<<16 | 'p'<<8 | 'l'), aatLayoutFeatureTypeCharacterShape, aatLayoutFeatureSelectorSimplifiedCharacters, 16},
	{ot.Tag('s'<<24 | 's'<<16 | '0'<<8 | '1'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltOneOn, aatLayoutFeatureSelectorStylisticAltOneOff},
	{ot.Tag('s'<<24 | 's'<<16 | '0'<<8 | '2'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltTwoOn, aatLayoutFeatureSelectorStylisticAltTwoOff},
	{ot.Tag('s'<<24 | 's'<<16 | '0'<<8 | '3'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltThreeOn, aatLayoutFeatureSelectorStylisticAltThreeOff},
	{ot.Tag('s'<<24 | 's'<<16 | '0'<<8 | '4'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltFourOn, aatLayoutFeatureSelectorStylisticAltFourOff},
	{ot.Tag('s'<<24 | 's'<<16 | '0'<<8 | '5'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltFiveOn, aatLayoutFeatureSelectorStylisticAltFiveOff},
	{ot.Tag('s'<<24 | 's'<<16 | '0'<<8 | '6'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltSixOn, aatLayoutFeatureSelectorStylisticAltSixOff},
	{ot.Tag('s'<<24 | 's'<<16 | '0'<<8 | '7'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltSevenOn, aatLayoutFeatureSelectorStylisticAltSevenOff},
	{ot.Tag('s'<<24 | 's'<<16 | '0'<<8 | '8'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltEightOn, aatLayoutFeatureSelectorStylisticAltEightOff},
	{ot.Tag('s'<<24 | 's'<<16 | '0'<<8 | '9'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltNineOn, aatLayoutFeatureSelectorStylisticAltNineOff},
	{ot.Tag('s'<<24 | 's'<<16 | '1'<<8 | '0'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltTenOn, aatLayoutFeatureSelectorStylisticAltTenOff},
	{ot.Tag('s'<<24 | 's'<<16 | '1'<<8 | '1'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltElevenOn, aatLayoutFeatureSelectorStylisticAltElevenOff},
	{ot.Tag('s'<<24 | 's'<<16 | '1'<<8 | '2'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltTwelveOn, aatLayoutFeatureSelectorStylisticAltTwelveOff},
	{ot.Tag('s'<<24 | 's'<<16 | '1'<<8 | '3'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltThirteenOn, aatLayoutFeatureSelectorStylisticAltThirteenOff},
	{ot.Tag('s'<<24 | 's'<<16 | '1'<<8 | '4'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltFourteenOn, aatLayoutFeatureSelectorStylisticAltFourteenOff},
	{ot.Tag('s'<<24 | 's'<<16 | '1'<<8 | '5'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltFifteenOn, aatLayoutFeatureSelectorStylisticAltFifteenOff},
	{ot.Tag('s'<<24 | 's'<<16 | '1'<<8 | '6'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltSixteenOn, aatLayoutFeatureSelectorStylisticAltSixteenOff},
	{ot.Tag('s'<<24 | 's'<<16 | '1'<<8 | '7'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltSeventeenOn, aatLayoutFeatureSelectorStylisticAltSeventeenOff},
	{ot.Tag('s'<<24 | 's'<<16 | '1'<<8 | '8'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltEighteenOn, aatLayoutFeatureSelectorStylisticAltEighteenOff},
	{ot.Tag('s'<<24 | 's'<<16 | '1'<<8 | '9'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltNineteenOn, aatLayoutFeatureSelectorStylisticAltNineteenOff},
	{ot.Tag('s'<<24 | 's'<<16 | '2'<<8 | '0'), aatLayoutFeatureTypeStylisticAlternatives, aatLayoutFeatureSelectorStylisticAltTwentyOn, aatLayoutFeatureSelectorStylisticAltTwentyOff},
	{ot.Tag('s'<<24 | 'u'<<16 | 'b'<<8 | 's'), aatLayoutFeatureTypeVerticalPosition, aatLayoutFeatureSelectorInferiors, aatLayoutFeatureSelectorNormalPosition},
	{ot.Tag('s'<<24 | 'u'<<16 | 'p'<<8 | 's'), aatLayoutFeatureTypeVerticalPosition, aatLayoutFeatureSelectorSuperiors, aatLayoutFeatureSelectorNormalPosition},
	{ot.Tag('s'<<24 | 'w'<<16 | 's'<<8 | 'h'), aatLayoutFeatureTypeContextualAlternatives, aatLayoutFeatureSelectorSwashAlternatesOn, aatLayoutFeatureSelectorSwashAlternatesOff},
	{ot.Tag('t'<<24 | 'i'<<16 | 't'<<8 | 'l'), aatLayoutFeatureTypeStyleOptions, aatLayoutFeatureSelectorTitlingCaps, aatLayoutFeatureSelectorNoStyleOptions},
	{ot.Tag('t'<<24 | 'n'<<16 | 'a'<<8 | 'm'), aatLayoutFeatureTypeCharacterShape, aatLayoutFeatureSelectorTraditionalNamesCharacters, 16},
	{ot.Tag('t'<<24 | 'n'<<16 | 'u'<<8 | 'm'), aatLayoutFeatureTypeNumberSpacing, aatLayoutFeatureSelectorMonospacedNumbers, 4},
	{ot.Tag('t'<<24 | 'r'<<16 | 'a'<<8 | 'd'), aatLayoutFeatureTypeCharacterShape, aatLayoutFeatureSelectorTraditionalCharacters, 16},
	{ot.Tag('t'<<24 | 'w'<<16 | 'i'<<8 | 'd'), aatLayoutFeatureTypeTextSpacing, aatLayoutFeatureSelectorThirdWidthText, 7},
	{ot.Tag('u'<<24 | 'n'<<16 | 'i'<<8 | 'c'), aatLayoutFeatureTypeLetterCase, 14, 15},
	{ot.Tag('v'<<24 | 'a'<<16 | 'l'<<8 | 't'), aatLayoutFeatureTypeTextSpacing, aatLayoutFeatureSelectorAltProportionalText, 7},
	{ot.Tag('v'<<24 | 'e'<<16 | 'r'<<8 | 't'), aatLayoutFeatureTypeVerticalSubstitution, aatLayoutFeatureSelectorSubstituteVerticalFormsOn, aatLayoutFeatureSelectorSubstituteVerticalFormsOff},
	{ot.Tag('v'<<24 | 'h'<<16 | 'a'<<8 | 'l'), aatLayoutFeatureTypeTextSpacing, aatLayoutFeatureSelectorAltHalfWidthText, 7},
	{ot.Tag('v'<<24 | 'k'<<16 | 'n'<<8 | 'a'), aatLayoutFeatureTypeAlternateKana, aatLayoutFeatureSelectorAlternateVertKanaOn, aatLayoutFeatureSelectorAlternateVertKanaOff},
	{ot.Tag('v'<<24 | 'p'<<16 | 'a'<<8 | 'l'), aatLayoutFeatureTypeTextSpacing, aatLayoutFeatureSelectorAltProportionalText, 7},
	{ot.Tag('v'<<24 | 'r'<<16 | 't'<<8 | '2'), aatLayoutFeatureTypeVerticalSubstitution, aatLayoutFeatureSelectorSubstituteVerticalFormsOn, aatLayoutFeatureSelectorSubstituteVerticalFormsOff},
	{ot.Tag('v'<<24 | 'r'<<16 | 't'<<8 | 'r'), aatLayoutFeatureTypeVerticalSubstitution, 2, 3},
	{ot.Tag('z'<<24 | 'e'<<16 | 'r'<<8 | 'o'), aatLayoutFeatureTypeTypographicExtras, aatLayoutFeatureSelectorSlashedZeroOn, aatLayoutFeatureSelectorSlashedZeroOff},
}

const deletedGlyph = 0xFFFF

type aatClassCache = otLayoutMappingCache

/* Note: This context is used for kerning, even without AAT, hence the condition. */

type aatApplyContext struct {
	plan      *otShapePlan
	font      *Font
	face      Face
	buffer    *Buffer
	gdef      tables.GDEF
	ankrTable tables.Ankr

	rangeFlags    []rangeFlags
	subtableFlags GlyphMask

	bufferIsReversed bool
	// Caches
	usingBufferGlyphSet bool
	bufferGlyphSet      intSet // runes or glyphs

	firstSet          intSet        // readonly
	secondSet         intSet        // readonly
	machineClassCache aatClassCache // readonly
}

func newAatApplyContext(plan *otShapePlan, font *Font, buffer *Buffer) *aatApplyContext {
	var out aatApplyContext
	out.plan = plan
	out.font = font
	out.face = font.face
	out.buffer = buffer
	return &out
}

func (c *aatApplyContext) reverseBuffer() {
	c.buffer.Reverse()
	c.bufferIsReversed = !c.bufferIsReversed
}

func (c *aatApplyContext) setupBufferGlyphSet() {
	c.usingBufferGlyphSet = len(c.buffer.Info) >= 4
	if !c.usingBufferGlyphSet {
		return
	}
	c.buffer.collectGlyphs(&c.bufferGlyphSet)
}

func (c *aatApplyContext) bufferIntersectsMachine() bool {
	if c.usingBufferGlyphSet {
		return c.bufferGlyphSet.Intersects(c.firstSet)
	}

	// Faster for shorter buffers.
	for _, info := range c.buffer.Info {
		if c.firstSet.hasGlyph(info.Glyph) {
			return true
		}
	}
	return false
}

func (c *aatApplyContext) outputGlyphs(glyphs []GID) bool {
	if c.usingBufferGlyphSet {
		c.bufferGlyphSet.addGlyphs(glyphs)
	}
	for _, glyph := range glyphs {
		if glyph == deletedGlyph {
			c.buffer.scratchFlags |= bsfAatHasDeleted
			c.buffer.cur(0).setAatDeleted()
		} else {
			if c.gdef.GlyphClassDef != nil {
				c.buffer.cur(0).glyphProps = c.gdef.GlyphProps(gID(glyph))
			}
		}
		c.buffer.outputGlyphIndex(glyph)
	}
	return true
}

func (c *aatApplyContext) replaceGlyph(glyph GID) {
	if glyph == deletedGlyph {
		c.buffer.scratchFlags |= bsfAatHasDeleted
		c.buffer.cur(0).setAatDeleted()
	}

	if c.usingBufferGlyphSet {
		c.bufferGlyphSet.addGlyph(glyph)
	}
	if c.gdef.GlyphClassDef != nil {
		c.buffer.cur(0).glyphProps = c.gdef.GlyphProps(gID(glyph))
	}
	c.buffer.replaceGlyphIndex(glyph)
}

func (c *aatApplyContext) deleteGlyph() {
	c.buffer.scratchFlags |= bsfAatHasDeleted
	c.buffer.cur(0).setAatDeleted()
	c.buffer.replaceGlyphIndex(deletedGlyph)
}

func (c *aatApplyContext) replace_glyph_inplace(i int, glyph gID) {
	c.buffer.Info[i].Glyph = GID(glyph)
	if c.usingBufferGlyphSet {
		c.bufferGlyphSet.addGlyph(GID(glyph))
	}
	if c.gdef.GlyphClassDef != nil {
		c.buffer.Info[i].glyphProps = c.gdef.GlyphProps(glyph)
	}
}

func (c *aatApplyContext) hasAnyFlags(flag GlyphMask) bool {
	for _, fl := range c.rangeFlags {
		if fl.flags&flag != 0 {
			return true
		}
	}
	return false
}

// execute the state machine in AAT tables
type stateTableDriver struct {
	machine font.AATStateTable
}

func newStateTableDriver(machine font.AATStateTable, _ Face) stateTableDriver {
	return stateTableDriver{machine: machine}
}

func (s *stateTableDriver) getClass(glyph GID, cache *aatClassCache) uint16 {
	key := uint16(glyph)
	if v, ok := cache.get(key); ok {
		return v
	}
	v := s.machine.GetClass(glyph)
	cache.set(key, v)
	return v
}

// implemented by the subtables
type driverContext interface {
	inPlace() bool
	isActionable(entry tables.AATStateEntry) bool
	transition(buffer *Buffer, s stateTableDriver, entry tables.AATStateEntry)
}

func (s stateTableDriver) drive(c driverContext, ac *aatApplyContext) {
	const (
		stateStartOfText = uint16(0)
		classEndOfText   = uint16(0)
		DontAdvance      = 0x4000
	)
	buffer := ac.buffer
	if !c.inPlace() {
		buffer.clearOutput()
	}

	state := stateStartOfText
	// If there's only one range, we already checked the flag.
	var lastRange int = -1 // index in ac.rangeFlags, or -1
	if len(ac.rangeFlags) > 1 {
		lastRange = 0
	}
	startStateSafeToBreakEot := !c.isActionable(s.machine.GetEntry(stateStartOfText, classEndOfText))

	for buffer.idx = 0; ; {
		class := classEndOfText
		if buffer.idx < len(buffer.Info) {
			class = s.getClass(buffer.Info[buffer.idx].Glyph, &ac.machineClassCache)
		}
	resume:
		if debugMode {
			fmt.Printf("\t\tState machine - class %d at index %d\n", class, buffer.idx)
		}
		entry := s.machine.GetEntry(state, class)
		nextState := entry.NewState // we only supported extended table

		isNotEpsilonTransition := entry.Flags&DontAdvance == 0
		isNotActionable := !c.isActionable(entry)

		if lastRange != -1 {
			// This block is copied in NoncontextualSubtable::apply. Keep in sync.
			range_ := lastRange
			if buffer.idx < len(buffer.Info) {
				cluster := buffer.cur(0).Cluster
				for cluster < ac.rangeFlags[range_].clusterFirst {
					range_--
				}
				for cluster > ac.rangeFlags[range_].clusterLast {
					range_++
				}

				lastRange = range_
			}
			if !(ac.rangeFlags[range_].flags&ac.subtableFlags != 0) {
				if buffer.idx == len(buffer.Info) {
					break
				}

				state = stateStartOfText
				buffer.nextGlyph()
				continue
			}
		} else {
			// Fast path for when transitioning from start-state to start-state with
			// no action and advancing. Do so as long as the class remains the same.
			// This is common with runs of non-actionable glyphs.

			isNullTransition := state == stateStartOfText &&
				nextState == stateStartOfText &&
				startStateSafeToBreakEot &&
				isNotActionable &&
				isNotEpsilonTransition &&
				lastRange == -1

			if isNullTransition {
				oldKlass := class
				for class == oldKlass {
					c.transition(buffer, s, entry)

					if buffer.idx == len(buffer.Info) {
						break
					}

					buffer.nextGlyph()

					class = classEndOfText
					if buffer.idx < len(buffer.Info) {
						class = s.getClass(buffer.Info[buffer.idx].Glyph, &ac.machineClassCache)
					}
				}

				if buffer.idx == len(buffer.Info) {
					break
				}

				goto resume
			}
		}

		/* Conditions under which it's guaranteed safe-to-break before current glyph:
		 *
		 * 1. There was no action in this transition; and
		 *
		 * 2. If we break before current glyph, the results will be the same. That
		 *    is guaranteed if:
		 *
		 *    2a. We were already in start-of-text state; or
		 *
		 *    2b. We are epsilon-transitioning to start-of-text state; or
		 *
		 *    2c. Starting from start-of-text state seeing current glyph:
		 *
		 *        2c'. There won't be any actions; and
		 *
		 *        2c". We would end up in the same state that we were going to end up
		 *             in now, including whether epsilon-transitioning.
		 *
		 *    and
		 *
		 * 3. If we break before current glyph, there won't be any end-of-text action
		 *    after previous glyph.
		 *
		 * This triples the transitions we need to look up, but is worth returning
		 * granular unsafe-to-break results. See eg.:
		 *
		 *   https://github.com/harfbuzz/harfbuzz/issues/2860
		 */

		wouldbeEntry := s.machine.GetEntry(stateStartOfText, class)
		isSafeToBreak := /* 1. */ !c.isActionable(entry) &&
			/* 2. */
			(
			/* 2a. */
			state == stateStartOfText ||
				/* 2b. */
				((entry.Flags&DontAdvance != 0) && nextState == stateStartOfText) ||
				/* 2c. */
				(
				/* 2c'. */
				!c.isActionable(wouldbeEntry) &&
					/* 2c". */
					(nextState == wouldbeEntry.NewState) &&
					(entry.Flags&DontAdvance) == (wouldbeEntry.Flags&DontAdvance))) &&
			/* 3. */
			!c.isActionable(s.machine.GetEntry(state, classEndOfText))

		if !isSafeToBreak && buffer.backtrackLen() != 0 && buffer.idx < len(buffer.Info) {
			buffer.unsafeToBreakFromOutbuffer(buffer.backtrackLen()-1, buffer.idx+1)
		}

		c.transition(buffer, s, entry)

		state = nextState

		if debugMode {
			fmt.Printf("\t\tState machine - new state %d\n", state)
		}

		if buffer.idx == len(buffer.Info) {
			break
		}

		if isNotEpsilonTransition {
			buffer.nextGlyph()
		} else {
			if buffer.maxOps <= 0 {
				buffer.maxOps--
				buffer.nextGlyph()
			} else {
				buffer.maxOps--
			}
		}
	}

	if !c.inPlace() {
		buffer.swapBuffers()
	}
}

///////

type aatFeatureMapping struct {
	otFeatureTag      font.Tag
	aatFeatureType    aatLayoutFeatureType
	selectorToEnable  aatLayoutFeatureSelector
	selectorToDisable aatLayoutFeatureSelector
}

// FaatLayoutFindFeatureMapping fetches the AAT feature-and-selector combination that corresponds
// to a given OpenType feature tag, or `nil` if not found.
func aatLayoutFindFeatureMapping(tag font.Tag) *aatFeatureMapping {
	low, high := 0, len(featureMappings)
	for low < high {
		mid := low + (high-low)/2 // avoid overflow when computing mid
		p := featureMappings[mid].otFeatureTag
		if tag < p {
			high = mid
		} else if tag > p {
			low = mid + 1
		} else {
			return &featureMappings[mid]
		}
	}
	return nil
}

func (sp *otShapePlan) aatLayoutSubstitute(font *Font, buffer *Buffer, features []Feature) {
	var map_ aatMap
	if len(features) != 0 {
		builder := newAatMapBuilder(font.face.Font, sp.props)
		for _, feature := range features {
			builder.addFeature(feature)
		}
		builder.compile(&map_)
	} else {
		map_ = sp.aatMap
	}

	morx := font.face.Morx
	c := newAatApplyContext(sp, font, buffer)
	c.buffer.unsafeToConcat(0, maxInt)
	c.setupBufferGlyphSet()
	for i, chain := range morx {
		accel := font.morxAccels[i]
		c.rangeFlags = map_.chainFlags[i]
		c.applyMorx(chain, accel)
	}
	// NOTE: we dont support obsolete 'mort' table
}

const bsfAatHasDeleted = bsfShaper0

func aatLayoutRemoveDeletedGlyphs(buffer *Buffer) {
	if buffer.scratchFlags&bsfAatHasDeleted != 0 {
		buffer.deleteGlyphsInplace(func(info *GlyphInfo) bool { return info.isAatDeleted() })
	}
}

func (sp *otShapePlan) aatLayoutPosition(font *Font, buffer *Buffer) {
	kerx := font.face.Kerx

	c := newAatApplyContext(sp, font, buffer)
	c.ankrTable = font.face.Ankr
	c.setupBufferGlyphSet()
	c.applyKernx(kerx, font.kerxAccels)
}

func collectLookupGlyphs(dst *intSet, src tables.AatLookupMixed) {
	for _, r := range src.Coverage() {
		dst.addRange(uint32(r[0]), uint32(r[1]))
	}
}

func (sp *otShapePlan) aatLayoutTrack(font *Font, buffer *Buffer) {
	c := newAatApplyContext(sp, font, buffer)
	c.applyTrak(0)
}

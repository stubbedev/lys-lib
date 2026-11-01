# xkb

Package `xkb` is the XKB keyboard model in pure Go: keymap compilation from
rules and text, the key state machine, keysyms and Compose. Package
`xkb/registry` lists what a ruleset offers. Package `xkb/x11` is the small X11
client that `xkb.SetupX11` can read keymaps through.

It follows libxkbcommon 1.13, whose sources (MIT) it was written against, and
differential tests hold it to that library: every layout, variant, option and
model `evdev.xml` and `evdev.extras.xml` offer, in both keymap formats, with key
events, masks and latches driven through both.

## Keymap formats

`FormatTextV1` is the classic format, at most 4 layouts. `FormatTextV2` adds up
to 32 layouts, several actions per level, canonical virtual modifiers and the
`unlockOnPress`, `latchOnPress` and `lockOnRelease` action fields, and a latch
survives a key that does not break it while the latching key is held.

`NewKeymapFromNames` compiles in `FormatTextV2`, as `xkb_keymap_new_from_names`
does. `NewKeymapFromString` parses in `FormatTextV1`, what a client should use
for the keymap a compositor sends. `Keymap.Text` is always `FormatTextV1`, what a
compositor should send: anything the format cannot express degrades and logs an
error. Use `Keymap.AsString` to choose.

## Differences from libxkbcommon

These are deliberate, and each one is tested:

- There is no reference counting. `ref` and `unref` have no equivalent, and the
  garbage collector frees everything. Nothing holds an operating system resource.
- A variadic C function takes a Go variadic parameter.
- A file included several times is parsed once, so the warnings its parser logs
  appear once. Every other message is logged as many times as libxkbcommon logs
  it, with the same level and `[XKB-NNN]` code.
- One failure logs its codes in one chain rather than a line of context each, so
  error codes match libxkbcommon by presence and warnings by count. Messages are
  worded like libxkbcommon's without being byte for byte equal:
  masks print as numbers, and an error carries the same detail in the returned
  error value.
- An environment variable set to the empty string counts as unset.
- On a system with neither glibc locale data nor musl, a locale other than `C`
  and `POSIX` counts as not installed, as glibc's `newlocale` reports it.
- `xkb_x11_keymap_new_from_device` takes an `xcb_connection_t`. Here the
  connection is the `X11Conn` interface, so this package links no X client
  library: `xkb/x11` implements it, and any other connection can.

## Function coverage

| libxkbcommon | Go |
| --- | --- |
| `xkb_context_new` | `NewContext`, `NewContextFlags` |
| `xkb_context_ref`, `xkb_context_unref` | garbage collected |
| `xkb_context_get_user_data`, `xkb_context_set_user_data` | `Context.UserData`, `Context.SetUserData` |
| `xkb_context_include_path_append` | `Context.AppendIncludePath` |
| `xkb_context_include_path_append_default` | `Context.AppendDefaultIncludePaths` |
| `xkb_context_include_path_reset_defaults` | `Context.ResetDefaultIncludePaths` |
| `xkb_context_include_path_clear` | `Context.ClearIncludePaths` |
| `xkb_context_num_include_paths` | `Context.NumIncludePaths` |
| `xkb_context_include_path_get` | `Context.IncludePath`, `Context.IncludePaths` |
| `xkb_context_set_log_level`, `xkb_context_get_log_level` | `Context.SetLogLevel`, `Context.LogLevel` |
| `xkb_context_set_log_verbosity`, `xkb_context_get_log_verbosity` | `Context.SetLogVerbosity`, `Context.LogVerbosity` |
| `xkb_context_set_log_fn` | `Context.SetLogFunc` |
| `xkb_components_names_from_rules` | `Context.ComponentsFromNames` |
| `xkb_keymap_new_from_names` | `Context.NewKeymapFromNames` |
| `xkb_keymap_new_from_names2` | `Context.NewKeymapFromNamesFormat` |
| `xkb_keymap_new_from_rmlvo` | `RMLVOBuilder.NewKeymap` |
| `xkb_keymap_new_from_string` | `Context.NewKeymapFromString`, `Context.NewKeymapFromBuffer` |
| `xkb_keymap_new_from_buffer` | `Context.NewKeymapFromBuffer` |
| `xkb_keymap_new_from_file` | `Context.NewKeymapFromFile` |
| `xkb_keymap_ref`, `xkb_keymap_unref` | garbage collected |
| `xkb_keymap_get_as_string`, `xkb_keymap_get_as_string2` | `Keymap.AsString`, `Keymap.Text` |
| `xkb_keymap_min_keycode`, `xkb_keymap_max_keycode` | `Keymap.MinKeycode`, `Keymap.MaxKeycode` |
| `xkb_keymap_key_for_each` | `Keymap.KeyForEach` |
| `xkb_keymap_key_get_name`, `xkb_keymap_key_by_name` | `Keymap.KeyGetName`, `Keymap.KeyByName` |
| `xkb_keymap_num_mods`, `xkb_keymap_mod_get_name`, `xkb_keymap_mod_get_index` | `Keymap.NumMods`, `Keymap.ModName`, `Keymap.ModIndex` |
| `xkb_keymap_mod_get_mask`, `xkb_keymap_mod_get_mask2` | `Keymap.ModMaskOf`, `Keymap.ModMaskOfIndex` |
| `xkb_keymap_num_layouts`, `xkb_keymap_layout_get_name`, `xkb_keymap_layout_get_index` | `Keymap.NumLayouts`, `Keymap.LayoutName`, `Keymap.LayoutIndex` |
| `xkb_keymap_num_leds`, `xkb_keymap_led_get_name`, `xkb_keymap_led_get_index` | `Keymap.NumLEDs`, `Keymap.LEDName`, `Keymap.LEDIndex` |
| `xkb_keymap_num_layouts_for_key`, `xkb_keymap_num_levels_for_key` | `Keymap.NumLayoutsForKey`, `Keymap.NumLevels` |
| `xkb_keymap_key_get_mods_for_level` | `Keymap.KeyGetModsForLevel` |
| `xkb_keymap_key_get_syms_by_level` | `Keymap.KeySymsByLevel` |
| `xkb_keymap_key_repeats` | `Keymap.KeyRepeats` |
| `xkb_keycode_is_legal_ext`, `xkb_keycode_is_legal_x11` | `KeycodeIsLegalExt`, `KeycodeIsLegalX11` |
| `xkb_keysym_get_name`, `xkb_keysym_from_name` | `KeysymGetName`, `KeysymFromName` |
| `xkb_keysym_to_utf8`, `xkb_keysym_to_utf32`, `xkb_utf32_to_keysym` | `KeysymToUTF8`, `KeysymToUTF32`, `UTF32ToKeysym` |
| `xkb_keysym_to_upper`, `xkb_keysym_to_lower` | `KeysymToUpper`, `KeysymToLower` |
| `xkb_rmlvo_builder_new` | `Context.NewRMLVOBuilder` |
| `xkb_rmlvo_builder_append_layout`, `xkb_rmlvo_builder_append_option` | `RMLVOBuilder.AppendLayout`, `RMLVOBuilder.AppendOption` |
| `xkb_rmlvo_builder_ref`, `xkb_rmlvo_builder_unref` | garbage collected |
| `xkb_state_new`, `xkb_state_get_keymap` | `Keymap.NewState`, `State.Keymap` |
| `xkb_state_ref`, `xkb_state_unref` | garbage collected |
| `xkb_state_update_key`, `xkb_state_update_mask`, `xkb_state_update_latched_locked` | `State.UpdateKey`, `State.UpdateMask`, `State.UpdateLatchedLocked` |
| `xkb_state_key_get_syms`, `xkb_state_key_get_one_sym` | `State.KeyGetSyms`, `State.KeyGetOneSym` |
| `xkb_state_key_get_utf8`, `xkb_state_key_get_utf32` | `State.KeyGetUTF8`, `State.AppendKeyUTF8`, `State.KeyGetUTF32` |
| `xkb_state_key_get_layout`, `xkb_state_key_get_level` | `State.KeyGetLayout`, `State.KeyGetLevel` |
| `xkb_state_serialize_mods`, `xkb_state_serialize_layout` | `State.SerializeMods`, `State.SerializeLayout` |
| `xkb_state_mod_name_is_active`, `xkb_state_mod_index_is_active` | `State.ModNameIsActive`, `State.ModIndexIsActive` |
| `xkb_state_mod_names_are_active`, `xkb_state_mod_indices_are_active` | `State.ModNamesAreActive`, `State.ModIndicesAreActive` |
| `xkb_state_layout_name_is_active`, `xkb_state_layout_index_is_active` | `State.LayoutNameIsActive`, `State.LayoutIndexIsActive` |
| `xkb_state_led_name_is_active`, `xkb_state_led_index_is_active` | `State.LEDNameIsActive`, `State.LEDIndexIsActive` |
| `xkb_state_key_get_consumed_mods`, `xkb_state_key_get_consumed_mods2` | `State.KeyGetConsumedMods` |
| `xkb_state_mod_index_is_consumed`, `xkb_state_mod_index_is_consumed2` | `State.ModIndexIsConsumed` |
| `xkb_state_mod_mask_remove_consumed` | `State.ModMaskRemoveConsumed` |
| `xkb_compose_table_new_from_locale`, `xkb_compose_table_new_from_buffer` | `Context.NewComposeTableFromLocale`, `Context.NewComposeTableFromBuffer` |
| `xkb_compose_table_new_from_file` | `Context.NewComposeTableFromFile`, `Context.NewComposeTableFromReader` |
| `xkb_compose_table_ref`, `xkb_compose_table_unref` | garbage collected |
| `xkb_compose_table_iterator_new`, `xkb_compose_table_iterator_next`, `xkb_compose_table_iterator_free` | `ComposeTable.Entries` |
| `xkb_compose_table_entry_sequence`, `xkb_compose_table_entry_keysym`, `xkb_compose_table_entry_utf8` | `ComposeEntry` |
| `xkb_compose_state_new`, `xkb_compose_state_get_compose_table` | `ComposeTable.NewState`, `ComposeState.Table` |
| `xkb_compose_state_ref`, `xkb_compose_state_unref` | garbage collected |
| `xkb_compose_state_feed`, `xkb_compose_state_reset` | `ComposeState.Feed`, `ComposeState.Reset` |
| `xkb_compose_state_get_status`, `xkb_compose_state_get_utf8`, `xkb_compose_state_get_one_sym` | `ComposeState.Status`, `ComposeState.UTF8`, `ComposeState.OneSym` |
| `xkb_x11_setup_xkb_extension` | `SetupX11` |
| `xkb_x11_get_core_keyboard_device_id` | `X11.CoreKeyboardDeviceID` |
| `xkb_x11_keymap_new_from_device`, `xkb_x11_state_new_from_device` | `X11.NewKeymapFromDevice`, `X11.NewStateFromDevice` |
| `rxkb_context_new`, `rxkb_context_ref`, `rxkb_context_unref` | `registry.NewContext`, garbage collected |
| `rxkb_context_set_log_fn`, `rxkb_context_set_log_level`, `rxkb_context_get_log_level` | `Context.SetLogFunc`, `Context.SetLogLevel`, `Context.LogLevel` |
| `rxkb_context_set_user_data`, `rxkb_context_get_user_data` | `Context.SetUserData`, `Context.UserData` |
| `rxkb_context_include_path_append`, `rxkb_context_include_path_append_default` | `Context.AppendIncludePath`, `Context.AppendDefaultIncludePaths` |
| `rxkb_context_parse`, `rxkb_context_parse_default_ruleset` | `Context.Parse`, `Context.ParseDefaultRuleset` |
| `rxkb_model_first`, `rxkb_model_next` | `Context.Models` |
| `rxkb_model_get_name`, `rxkb_model_get_description`, `rxkb_model_get_vendor`, `rxkb_model_get_popularity` | `Model` fields |
| `rxkb_model_ref`, `rxkb_model_unref` | garbage collected |
| `rxkb_layout_first`, `rxkb_layout_next` | `Context.Layouts` |
| `rxkb_layout_get_name`, `rxkb_layout_get_variant`, `rxkb_layout_get_brief`, `rxkb_layout_get_description`, `rxkb_layout_get_popularity` | `Layout` fields |
| `rxkb_layout_get_iso639_first`, `rxkb_iso639_code_next`, `rxkb_iso639_code_get_code` | `Layout.ISO639` |
| `rxkb_layout_get_iso3166_first`, `rxkb_iso3166_code_next`, `rxkb_iso3166_code_get_code` | `Layout.ISO3166` |
| `rxkb_layout_ref`, `rxkb_layout_unref`, `rxkb_iso639_code_ref`, `rxkb_iso639_code_unref`, `rxkb_iso3166_code_ref`, `rxkb_iso3166_code_unref` | garbage collected |
| `rxkb_option_group_first`, `rxkb_option_group_next` | `Context.OptionGroups` |
| `rxkb_option_group_get_name`, `rxkb_option_group_get_description`, `rxkb_option_group_get_popularity`, `rxkb_option_group_allows_multiple` | `OptionGroup` fields |
| `rxkb_option_group_ref`, `rxkb_option_group_unref` | garbage collected |
| `rxkb_option_first`, `rxkb_option_next` | `OptionGroup.Options` |
| `rxkb_option_get_name`, `rxkb_option_get_brief`, `rxkb_option_get_description`, `rxkb_option_get_popularity`, `rxkb_option_is_layout_specific` | `Option` fields |
| `rxkb_option_ref`, `rxkb_option_unref` | garbage collected |

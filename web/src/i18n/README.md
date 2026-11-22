# English UI text

SOCPuppet ships an English-only interface. The English source string passed to
`t()` is returned directly, with `{name}` placeholders interpolated at runtime.
Date and number formatting use the fixed `en-US` locale.

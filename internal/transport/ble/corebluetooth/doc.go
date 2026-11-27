// Package corebluetooth installs the macOS Bluetooth stack (tinygo.org/x/bluetooth over
// CoreBluetooth) as the backend of internal/transport/ble. Import it for its side effect
// from the main package only:
//
//	import _ "github.com/ljagiello/crate-labels/internal/transport/ble/corebluetooth"
//
// Linking tinygo's bluetooth package allocates a CBCentralManager at program start,
// so nothing that tests import may import this package. The adapter itself is enabled
// lazily, on the first scan or connection. On other platforms the package does nothing
// and ble reports that Bluetooth is unavailable.
package corebluetooth

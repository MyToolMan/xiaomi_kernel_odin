#!/system/bin/sh

ui_print "- Installing ODIN Tuner skeleton"
set_perm_recursive "$MODPATH" 0 0 0755 0644
set_perm "$MODPATH/service.sh" 0 0 0755
set_perm "$MODPATH/uninstall.sh" 0 0 0755
set_perm "$MODPATH/action.sh" 0 0 0755
set_perm "$MODPATH/bin/odin-tuner" 0 0 0755
set_perm "$MODPATH/bin/start-web.sh" 0 0 0755
set_perm "$MODPATH/bin/stop-web.sh" 0 0 0755
ui_print "- Root daemon starts after the next boot"
ui_print "- Web UI is started on demand from the module action"

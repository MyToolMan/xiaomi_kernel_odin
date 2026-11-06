#!/system/bin/sh

ui_print "- Installing ODIN Super Panel"
set_perm_recursive "$MODPATH" 0 0 0755 0644
set_perm "$MODPATH/service.sh" 0 0 0755
set_perm "$MODPATH/uninstall.sh" 0 0 0755
set_perm "$MODPATH/action.sh" 0 0 0755
set_perm "$MODPATH/bin/odin-tuner" 0 0 0755
set_perm "$MODPATH/bin/start-web.sh" 0 0 0755
set_perm "$MODPATH/bin/stop-web.sh" 0 0 0755
set_perm "$MODPATH/bin/restart-web.sh" 0 0 0755
ui_print "- Root daemon starts after the next boot"
ui_print "- Startup performance profile releases existing CPU/GPU OPP limits"
ui_print "- Web service autostart follows config.json web_autostart"
ui_print "- Module action only restarts the Web service"

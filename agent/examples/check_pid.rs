use sysinfo::{System, SystemExt, ProcessExt};
fn main() {
    let s = System::new_all();
    for (pid, proc) in s.processes().iter().take(1) {
        println!("Pid type name: {}", std::any::type_name::<sysinfo::Pid>());
        println!("pid debug: {:?}", pid);
        println!("proc.pid(): {:?}", proc.pid());
        // Try size_of
        println!("Pid size: {}", std::mem::size_of::<sysinfo::Pid>());
    }
}

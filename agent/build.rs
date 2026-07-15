fn main() -> Result<(), Box<dyn std::error::Error>> {
    tonic_build::compile_protos("../proto/baize/v1/baize.proto")?;
    Ok(())
}

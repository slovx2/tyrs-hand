pub fn write_number(value: u64) -> String {
    let mut buffer = itoa::Buffer::new();
    buffer.format(value).to_owned()
}

#[cfg(test)]
mod tests {
    #[test]
    fn runtime_is_ready() {
        assert_eq!(super::write_number(42), "42");
    }
}

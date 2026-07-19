pub fn ready() -> bool {
    itoa::Buffer::new().format(42) == "42"
}

#[cfg(test)]
mod tests {
    #[test]
    fn runtime_is_ready() {
        assert!(super::ready());
    }
}
